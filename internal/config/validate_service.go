package config

import (
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/dockerctl"
	"sermo/internal/process"
	"sermo/internal/virt"
)

var validMonitorModes = set(MonitorEnabled, MonitorDisabled, MonitorPrevious)
var validProcessSelectorKeys = set("exe", "cmd", "user", "group", "delete", keyEnableIf)

func validateMonitorMode(path string, mode any, add addFunc) {
	s, isStr := mode.(string)
	if _, ok := validMonitorModes[s]; !isStr || !ok {
		add("%s %q is not one of enabled, disabled, previous", path, cfgval.String(mode))
	}
}

// validateServiceMonitors validates the per-service `version:`/`config:` monitor
// blocks: their `on_change.notify` selection must reference defined notifiers (or
// the `none` sentinel). The version/config commands themselves are reused from
// the catalog service (commands.version / preflight.config) and validated there.
func validateServiceMonitors(tree map[string]any, notifiers map[string]struct{}, add addFunc) {
	for _, key := range []string{"version", "config"} {
		block, ok := tree[key].(map[string]any)
		if !ok {
			continue
		}
		oc, present := block["on_change"]
		if !present {
			continue
		}
		ocMap, ok := oc.(map[string]any)
		if !ok {
			add("%s.on_change must be a mapping", key)
			continue
		}
		if _, present := ocMap["notify"]; present {
			validateNotifySelection(key+".on_change.notify", ocMap["notify"], notifiers, add)
		}
		// `level` selects version-change granularity and only applies to the
		// version monitor, which compares version_short at major/minor/patch.
		if lv, present := ocMap["level"]; present {
			if key != "version" {
				add("%s.on_change.level is only supported for the version monitor", key)
			} else if _, ok := checks.VersionLevel(cfgval.String(lv)); !ok {
				add("version.on_change.level %q is not one of major, minor, patch", cfgval.String(lv))
			}
		}
	}
}

func validateStopPolicy(tree map[string]any, add addFunc) {
	sp, ok := tree[sectionStopPolicy].(map[string]any)
	if !ok {
		return
	}
	for _, field := range []string{"graceful_timeout", "term_timeout", "kill_timeout"} {
		if v, present := sp[field]; present && !isPositiveDuration(cfgval.String(v)) {
			add("stop_policy.%s %q must be a valid positive duration", field, cfgval.String(v))
		}
	}
	force, _ := sp["force_kill"].(bool)
	koi, hasKoi := sp["kill_only_if"].(map[string]any)
	if force && !hasKoi {
		add("stop_policy.force_kill=true requires kill_only_if")
	}
	if hasKoi {
		if !cfgval.IsNonEmptyStringList(koi["users"]) || !cfgval.IsNonEmptyStringList(koi["exe_any"]) {
			add("stop_policy.kill_only_if must define both users and exe_any, each non-empty")
		}
	}
	// Stopped-state invariants (verified after a clean stop). clean_after_stop is
	// the master opt-in that enables deleting stale leftovers and the clean_on_stop
	// list; with it off the invariants are only verified and warned about.
	for _, b := range []string{"pidfile_absent", "clean_after_stop"} {
		if v, present := sp[b]; present {
			if _, ok := v.(bool); !ok {
				add("stop_policy.%s must be true or false", b)
			}
		}
	}
	if v, present := sp["files_absent"]; present && !cfgval.IsNonEmptyStringList(v) {
		add("stop_policy.files_absent must be a non-empty list of paths/globs")
	}
	validateCleanOnStop(sp["clean_on_stop"], add)
}

// protectedDirs are absolute paths that clean_on_stop must never delete
// recursively (the filesystem root and shallow system directories).
var protectedDirs = set("/", "/etc", "/usr", "/var", "/home", "/root", "/boot",
	"/bin", "/sbin", "/lib", "/lib32", "/lib64", "/proc", "/sys", "/dev", "/run",
	"/tmp", "/opt", "/srv", "/mnt", "/media", "/usr/bin", "/usr/sbin", "/usr/lib",
	"/usr/local", "/var/lib", "/var/log")

// validateCleanOnStop validates the clean_on_stop list: each entry is a path
// string or a {path, recursive} mapping; paths must be absolute, and a recursive
// entry must be a concrete (non-glob) path at least two levels deep and not a
// protected system directory.
func validateCleanOnStop(raw any, add addFunc) {
	if raw == nil {
		return
	}
	list, ok := raw.([]any)
	if !ok {
		add("stop_policy.clean_on_stop must be a list")
		return
	}
	for i, item := range list {
		var path string
		var recursive bool
		switch e := item.(type) {
		case string:
			path = e
		case map[string]any:
			path = cfgval.AsString(e["path"])
			if rawRecursive, present := e["recursive"]; present {
				var ok bool
				recursive, ok = rawRecursive.(bool)
				if !ok {
					add("stop_policy.clean_on_stop[%d].recursive must be a boolean", i)
				}
			}
		default:
			add("stop_policy.clean_on_stop[%d] must be a path or a {path, recursive} mapping", i)
			continue
		}
		if path == "" {
			add("stop_policy.clean_on_stop[%d] has an empty path", i)
			continue
		}
		if !filepath.IsAbs(path) {
			add("stop_policy.clean_on_stop[%d] path %q must be absolute", i, path)
			continue
		}
		if recursive {
			// filepath.Clean also collapses ".." segments, so a path like
			// /var/log/../.. cannot sidestep the protected-dir check.
			clean := filepath.Clean(path)
			_, isProtected := protectedDirs[clean]
			if strings.ContainsAny(path, "*?[") {
				add("stop_policy.clean_on_stop[%d] recursive path %q must not be a glob", i, path)
			} else if isProtected || pathDepth(clean) < 2 {
				add("stop_policy.clean_on_stop[%d] refuses to recursively delete %q (root/shallow/system path)", i, path)
			}
		}
	}
}

// pathDepth counts the path components below root of a filepath.Clean'ed
// absolute path ("/var/cache" -> 2, "/" -> 0).
func pathDepth(p string) int {
	if p == "/" {
		return 0
	}
	return strings.Count(p, "/")
}

func validateProcesses(tree map[string]any, add addFunc) {
	processes, ok := tree[sectionProcesses].(map[string]any)
	if !ok {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(processes)) {
		path := "processes." + name
		entry, ok := processes[name].(map[string]any)
		if !ok {
			add("%s must be a mapping", path)
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(entry)) {
			if _, ok := validProcessSelectorKeys[key]; !ok {
				add("%s.%s is not supported; processes entries accept exe, cmd, user, group and enable_if", path, key)
			}
		}
		exe, cmd := cfgval.String(entry["exe"]), cfgval.String(entry["cmd"])
		if exe == "" && cmd == "" {
			add("%s requires exe or cmd", path)
		}
		if cmd != "" {
			if _, err := regexp.Compile(cmd); err != nil {
				add("%s.cmd is not a valid regex: %v", path, err)
			}
		}
	}
}

func validatePidfiles(tree map[string]any, add addFunc) {
	raw, present := tree["pidfiles"]
	if _, hasPidfile := tree["pidfile"]; hasPidfile && present {
		add("pidfile and pidfiles are mutually exclusive")
	}
	if !present {
		return
	}
	pidfiles, ok := raw.(map[string]any)
	if !ok {
		add("pidfiles must be a mapping of process role to path string or candidate list")
		return
	}
	processes, _ := tree[sectionProcesses].(map[string]any)
	for _, role := range slices.Sorted(maps.Keys(pidfiles)) {
		if !validDocumentName(role) {
			add("pidfiles.%s role must be a simple name without path separators", role)
			continue
		}
		paths := cfgval.StringList(pidfiles[role])
		if len(paths) == 0 {
			add("pidfiles.%s must be a non-empty path string or list", role)
			continue
		}
		for _, path := range paths {
			if !filepath.IsAbs(path) {
				add("pidfiles.%s path %q must be absolute", role, path)
			}
		}
		entry, ok := processes[role].(map[string]any)
		if !ok {
			add("pidfiles.%s requires matching processes.%s", role, role)
			continue
		}
		if cfgval.String(entry["exe"]) == "" {
			add("pidfiles.%s requires processes.%s.exe for exact pidfile identity", role, role)
		}
		if cfgval.String(entry["user"]) == "" {
			add("pidfiles.%s requires processes.%s.user for exact pidfile identity", role, role)
		}
	}
}

func validatePolicyExtras(tree map[string]any, add addFunc) {
	policy, ok := tree["policy"].(map[string]any)
	if !ok {
		return
	}
	if v, present := policy["max_actions"]; present {
		if n, ok := cfgval.Int(v); !ok || n <= 0 {
			add("policy.max_actions must be an integer > 0")
		}
		if _, hasWindow := policy["max_actions_window"]; !hasWindow {
			add("policy.max_actions requires policy.max_actions_window")
		}
	}
	if v, present := policy["max_actions_window"]; present && !isPositiveDuration(cfgval.String(v)) {
		add("policy.max_actions_window %q must be a valid positive duration", cfgval.String(v))
	}
	if bo, ok := policy["backoff"].(map[string]any); ok {
		initial := cfgval.String(bo["initial"])
		maxStr := cfgval.String(bo["max"])
		initialOK := isPositiveDuration(initial)
		maxOK := isPositiveDuration(maxStr)
		if !initialOK {
			add("policy.backoff.initial must be a valid positive duration")
		}
		if !maxOK {
			add("policy.backoff.max must be a valid positive duration")
		}
		// Only compare once both parse cleanly: otherwise a garbage initial
		// (di defaulting to 0) would let any max pass, and an omitted max would
		// report the misleading ">= initial" instead of its own parse error.
		if initialOK && maxOK {
			di, _ := time.ParseDuration(initial)
			dm, _ := time.ParseDuration(maxStr)
			if dm < di {
				add("policy.backoff.max must be >= initial")
			}
		}
	}
}

func validateControl(tree map[string]any, add addFunc) {
	raw, present := tree["control"]
	if !present {
		return
	}
	control, ok := raw.(map[string]any)
	if !ok {
		add("control must be a mapping")
		return
	}
	typ := cfgval.String(control["type"])
	switch typ {
	case "libvirt":
		validateControlKeys(control, set("type", "uri", "domain", "uuid", "socket", "host", "port"), "type, uri, domain, uuid, socket, host, port", add)
		validateLibvirtControl(control, add)
	case "docker":
		validateControlKeys(control, set("type", "socket", "host", "port", "tls", "container"), "type, socket, host, port, tls, container", add)
		validateDockerControl(control, add)
	default:
		add("control.type %q is not one of libvirt, docker", typ)
	}
}

func validateControlKeys(control map[string]any, allowed map[string]struct{}, labels string, add addFunc) {
	for _, key := range slices.Sorted(maps.Keys(control)) {
		if _, ok := allowed[key]; !ok {
			add("control key %q is not one of %s", key, labels)
		}
	}
}

func validateLibvirtControl(control map[string]any, add addFunc) {
	if domain := cfgval.String(control["domain"]); domain == "" {
		add("control.domain is required for libvirt")
	}
	if uri := cfgval.String(control["uri"]); uri != "" && strings.TrimSpace(uri) == "" {
		add("control.uri must not be blank")
	}
	if uuid := cfgval.String(control["uuid"]); uuid != "" {
		if _, err := virt.ParseUUID(uuid); err != nil {
			add("control.uuid %q must be a canonical UUID or 32 hex digits", uuid)
		}
	}
	if socket := cfgval.String(control["socket"]); socket != "" && !virt.ValidSocketPath(socket) {
		add("control.socket %q must be an absolute path", socket)
	}
	host := cfgval.String(control["host"])
	if host != "" && strings.TrimSpace(host) == "" {
		add("control.host must not be blank")
	}
	if host != "" && cfgval.String(control["socket"]) != "" {
		add("control must not set both socket and host")
	}
	if _, present := control["port"]; present {
		port, ok := cfgval.Int(control["port"])
		if !ok || !virt.ValidHostPort(host, port) {
			add("control.port must be an integer in 1..65535")
		}
	}
}

func validateDockerControl(control map[string]any, add addFunc) {
	if container := cfgval.String(control["container"]); container == "" {
		add("control.container is required for docker")
	}
	if socket := cfgval.String(control["socket"]); socket != "" && !filepath.IsAbs(socket) {
		add("control.socket %q must be an absolute path", socket)
	}
	host := cfgval.String(control["host"])
	if host != "" && strings.TrimSpace(host) == "" {
		add("control.host must not be blank")
	}
	if host != "" && cfgval.String(control["socket"]) != "" {
		add("control must not set both socket and host")
	}
	if _, present := control["port"]; present {
		port, ok := cfgval.Int(control["port"])
		if !ok || port < 1 || port > 65535 {
			add("control.port must be an integer in 1..65535")
		}
	}
	if !dockerctl.ValidTLSValue(control["tls"]) {
		add("control.tls %q is not one of true, false, required, skip-verify", cfgval.String(control["tls"]))
	}
}

// validateReload checks the optional `reload:` block: a native reload Sermo runs
// when the init backend cannot (`when: auto`) or instead of it (`when: always`).
// Exactly one of `signal` (a known signal name) or `command` (an array) is
// required; `when`, when present, must be `auto` or `always`. A signal reload
// for a backend without a MainPID source also needs top-level pidfile: plus a
// process selector with exact exe and user so the signal target can be verified
// before signaling.
func validateReload(tree map[string]any, backend string, add addFunc) {
	raw, present := tree["reload"]
	if !present {
		return
	}
	r, ok := raw.(map[string]any)
	if !ok {
		add("reload must be a mapping with a signal or command")
		return
	}
	if when := cfgval.AsString(r["when"]); when != "" && when != "auto" && when != "always" {
		add("reload.when %q must be \"auto\" or \"always\"", when)
	}
	sig := cfgval.AsString(r["signal"])
	_, hasCmd := r["command"]
	switch {
	case sig != "" && hasCmd:
		add("reload sets both signal and command; use exactly one")
	case sig != "":
		if _, err := process.ParseSignal(sig); err != nil {
			add("reload.signal %q is not a known signal name (%s)", sig, strings.Join(process.SignalNames(), ", "))
		} else if reloadSignalNeedsPidfileIdentity(tree, backend) {
			pidfile, identity := reloadSignalPidfileIdentity(tree)
			if !pidfile {
				add("reload.signal requires top-level pidfile: when the service runs on OpenRC (no MainPID)")
			}
			if !identity {
				add("reload.signal requires a processes selector with both exe and user so the pidfile PID can be verified before signaling")
			}
		}
	case hasCmd:
		if !cfgval.IsNonEmptyStringArray(r["command"]) {
			add("reload.command must be an array, not a shell string")
		} else if len(cfgval.StringArray(r["command"])) == 0 {
			add("reload.command must not be empty")
		}
	default:
		add("reload must set either signal or command")
	}
}

// reloadSignalNeedsPidfileIdentity reports whether a signal-based reload
// requires pidfile-identity verification: OpenRC-capable or backend-neutral
// services need it because OpenRC has no MainPID source. Systemd-only services
// can rely on the backend MainPID path instead.
func reloadSignalNeedsPidfileIdentity(tree map[string]any, backend string) bool {
	svc, ok := tree["service"].(map[string]any)
	if !ok {
		return backend == backendOpenRC
	}
	_, hasSystemd := svc[backendSystemd]
	_, hasOpenrc := svc[backendOpenRC]
	if backend == backendOpenRC {
		return hasOpenrc || !hasSystemd
	}
	return hasOpenrc && !hasSystemd
}

func reloadSignalPidfileIdentity(tree map[string]any) (pidfile, identity bool) {
	pidfile = len(cfgval.StringList(tree["pidfile"])) > 0
	procs, ok := tree[sectionProcesses].(map[string]any)
	if !ok {
		return pidfile, false
	}
	for _, raw := range procs {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if cfgval.String(entry["exe"]) != "" && cfgval.String(entry["user"]) != "" {
			identity = true
		}
	}
	return pidfile, identity
}

// validateCommands checks the optional `commands` section: each entry uses array
// form with an optional valid duration timeout and output expectations.
// Reserved names are consumed by features — `health`, `version`
// and `version_short` by the apps listings, and `version` by the
// version.on_change monitor; any other entry is informational.
func validateCommands(tree map[string]any, add addFunc) {
	commands, ok := tree["commands"].(map[string]any)
	if !ok {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(commands)) {
		entry, ok := commands[name].(map[string]any)
		if !ok {
			add("commands.%s must be a mapping", name)
			continue
		}
		path := "commands." + name
		validateCommandFields(path, entry, false, add)
		if v, present := entry["timeout"]; present && !isPositiveDuration(cfgval.String(v)) {
			add("commands.%s timeout %q must be a valid positive duration", name, cfgval.String(v))
		}
	}
}

// validateServiceField checks the `service` field: a scalar unit name or a
// per-init map of systemd/openrc candidate lists.
func validateServiceField(tree map[string]any, add addFunc) {
	s, present := tree["service"]
	if !present {
		return
	}
	switch v := s.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			add("service must not be empty")
		}
	case map[string]any:
		if len(v) == 0 {
			add("service map must define systemd and/or openrc")
		}
		for _, k := range slices.Sorted(maps.Keys(v)) {
			switch k {
			case backendSystemd, backendOpenRC:
				if !cfgval.IsNonEmptyStringArray(v[k]) {
					add("service.%s must be a non-empty list", k)
				}
			default:
				add("service key %q is not one of systemd, openrc", k)
			}
		}
	default:
		add("service must be a unit name or a per-init map (systemd/openrc)")
	}
}

// validateAlsoService checks the `also_service` field: a per-init map of
// systemd/openrc unit lists (auxiliary units acted on with the primary). Units
// must be non-empty and must not repeat the primary `service` unit for that
// backend (a self-reference).
func validateAlsoService(tree map[string]any, add addFunc) {
	a, present := tree["also_service"]
	if !present {
		return
	}
	m, ok := a.(map[string]any)
	if !ok {
		add("also_service must be a per-init map (systemd/openrc)")
		return
	}
	svc, _ := tree["service"].(map[string]any)
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if k != backendSystemd && k != backendOpenRC {
			add("also_service key %q is not one of systemd, openrc", k)
			continue
		}
		units := cfgval.StringList(m[k])
		if !cfgval.IsNonEmptyStringArray(m[k]) {
			add("also_service.%s must be a non-empty list", k)
		}
		primary := map[string]bool{}
		if svc != nil {
			for _, u := range cfgval.StringList(svc[k]) {
				primary[u] = true
			}
		}
		for _, u := range units {
			if strings.TrimSpace(u) == "" {
				add("also_service.%s contains an empty unit", k)
			}
			if primary[u] {
				add("also_service.%s lists %q, which is the primary service unit", k, u)
			}
		}
	}
}

// validateCascade checks `also_apply`: each entry must be a known service and not
// the service itself. Targets receive the same action via their own operation.
func validateCascade(name string, tree map[string]any, services map[string]struct{}, add addFunc) {
	targets, err := cfgval.StrictStringList(tree["also_apply"])
	if _, present := tree["also_apply"]; present && err != nil {
		add("also_apply must be a string or list of strings")
		return
	}
	for _, target := range targets {
		if target == "" {
			add("also_apply contains an empty service name")
			continue
		}
		if target == name {
			add("also_apply lists %q, the service itself", target)
			continue
		}
		if _, ok := services[target]; !ok {
			add("also_apply references %q, which is not a configured service", target)
		}
	}
}

func policyCooldown(tree map[string]any) (string, bool) {
	policy, ok := tree["policy"].(map[string]any)
	if !ok {
		return "", false
	}
	v, present := policy["cooldown"]
	if !present {
		return "", false
	}
	return cfgval.String(v), true
}
