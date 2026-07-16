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
	"sermo/internal/rules"
	"sermo/internal/virt"
)

const (
	controlTypeSummary      = virt.ControlType + ", " + dockerctl.ControlType
	dockerControlKeySummary = dockerctl.ControlKeyType + ", " +
		dockerctl.ControlKeySocket + ", " +
		dockerctl.ControlKeyHost + ", " +
		dockerctl.ControlKeyPort + ", " +
		dockerctl.ControlKeyTLS + ", " +
		dockerctl.ControlKeyContainer
	libvirtControlKeySummary = virt.ControlKeyType + ", " +
		virt.ControlKeyURI + ", " +
		virt.ControlKeyDomain + ", " +
		virt.ControlKeyUUID + ", " +
		virt.ControlKeySocket + ", " +
		virt.ControlKeyHost + ", " +
		virt.ControlKeyPort
	controlSocketHostConflictMessage = "control must not set both socket and host"
)

var validMonitorModes = set(MonitorEnabled, MonitorDisabled, MonitorPrevious)

var validProcessSelectorKeys = set(
	process.SelectorKeyExe,
	process.SelectorKeyCmd,
	process.SelectorKeyUser,
	process.SelectorKeyGroup,
	keyDelete,
	keyEnableIf,
)

func validateMonitorMode(path string, mode any, add addFunc) {
	s, isStr := mode.(string)
	if _, ok := validMonitorModes[s]; !isStr || !ok {
		add("%s %q is not one of %s", path, cfgval.String(mode), MonitorModeSummary)
	}
}

// validateServiceMonitors validates the per-service `version:`/`config:` monitor
// blocks: their `on_change.notify` selection must reference defined notifiers (or
// the `none` sentinel). The version/config commands themselves are reused from
// the catalog service (commands.version / preflight.config) and validated there.
func validateServiceMonitors(tree map[string]any, notifiers map[string]struct{}, add addFunc) {
	for _, key := range []string{ServiceMonitorKeyVersion, ServiceMonitorKeyConfig} {
		block, ok := tree[key].(map[string]any)
		if !ok {
			continue
		}
		oc, present := block[ServiceMonitorKeyOnChange]
		if !present {
			continue
		}
		ocMap, ok := oc.(map[string]any)
		if !ok {
			add("%s must be a mapping", serviceMonitorOnChangePath(key))
			continue
		}
		if _, present := ocMap[rules.RuleFieldNotify]; present {
			validateNotifySelection(serviceMonitorOnChangeFieldPath(key, rules.RuleFieldNotify), ocMap[rules.RuleFieldNotify], notifiers, add)
		}
		// `level` selects version-change granularity and only applies to the
		// version monitor, which compares version_short at major/minor/patch.
		if lv, present := ocMap[ServiceMonitorKeyLevel]; present {
			if key != ServiceMonitorKeyVersion {
				add("%s is only supported for the version monitor", serviceMonitorOnChangeFieldPath(key, ServiceMonitorKeyLevel))
			} else if _, ok := checks.VersionLevel(cfgval.String(lv)); !ok {
				add("%s %q is not one of %s", serviceMonitorOnChangeFieldPath(ServiceMonitorKeyVersion, ServiceMonitorKeyLevel), cfgval.String(lv), checks.VersionLevelSummary)
			}
		}
	}
}

func validateStopPolicy(tree map[string]any, add addFunc) {
	sp, ok := tree[sectionStopPolicy].(map[string]any)
	if !ok {
		return
	}
	for _, field := range []string{keyGracefulTimeout, keyTermTimeout, keyKillTimeout} {
		if v, present := sp[field]; present && !isPositiveDuration(cfgval.String(v)) {
			add("%s %q must be a valid positive duration", stopPolicyFieldPath(field), cfgval.String(v))
		}
	}
	force, _ := sp[keyForceKill].(bool)
	koi, hasKoi := sp[keyKillOnlyIf].(map[string]any)
	if force && !hasKoi {
		add("%s=true requires %s", stopPolicyPathForceKill, keyKillOnlyIf)
	}
	if hasKoi {
		if !cfgval.IsNonEmptyStringList(koi[keyUsers]) || !cfgval.IsNonEmptyStringList(koi[keyExeAny]) {
			add("%s must define both %s and %s, each non-empty", stopPolicyPathKillOnlyIf, keyUsers, keyExeAny)
		}
	}
	// Stopped-state invariants (verified after a clean stop). clean_after_stop is
	// the master opt-in that enables deleting stale leftovers and the clean_on_stop
	// list; with it off the invariants are only verified and warned about.
	for _, b := range []string{keyPidfileAbsent, keyCleanAfterStop} {
		if v, present := sp[b]; present {
			if _, ok := v.(bool); !ok {
				add(validationBooleanLiteralFormat, stopPolicyFieldPath(b))
			}
		}
	}
	if v, present := sp[keyFilesAbsent]; present && !cfgval.IsNonEmptyStringList(v) {
		add("%s must be a non-empty list of paths/globs", stopPolicyPathFilesAbsent)
	}
	validateCleanOnStop(sp[keyCleanOnStop], add)
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
		add("%s must be a list", stopPolicyPathCleanOnStop)
		return
	}
	for i, item := range list {
		entryPath := stopPolicyCleanOnStopEntryPath(i)
		var path string
		var recursive bool
		switch e := item.(type) {
		case string:
			path = e
		case map[string]any:
			path = cfgval.AsString(e[keyPath])
			if rawRecursive, present := e[keyRecursive]; present {
				var ok bool
				recursive, ok = rawRecursive.(bool)
				if !ok {
					add(validationBooleanFormat, entryPath+"."+keyRecursive)
				}
			}
		default:
			add("%s must be a path or a {path, recursive} mapping", entryPath)
			continue
		}
		if path == "" {
			add("%s has an empty path", entryPath)
			continue
		}
		if !filepath.IsAbs(path) {
			add("%s path %q must be absolute", entryPath, path)
			continue
		}
		if recursive {
			// filepath.Clean also collapses ".." segments, so a path like
			// /var/log/../.. cannot sidestep the protected-dir check.
			clean := filepath.Clean(path)
			_, isProtected := protectedDirs[clean]
			if strings.ContainsAny(path, "*?[") {
				add("%s recursive path %q must not be a glob", entryPath, path)
			} else if isProtected || pathDepth(clean) < 2 {
				add("%s refuses to recursively delete %q (root/shallow/system path)", entryPath, path)
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
		path := processEntryPath(name)
		entry, ok := processes[name].(map[string]any)
		if !ok {
			add(validationMappingFormat, path)
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(entry)) {
			if _, ok := validProcessSelectorKeys[key]; !ok {
				add("%s.%s is not supported; processes entries accept exe, cmd, user, group and enable_if", path, key)
			}
		}
		exe, cmd := cfgval.String(entry[process.SelectorKeyExe]), cfgval.String(entry[process.SelectorKeyCmd])
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
	raw, present := tree[ServiceKeyPidfiles]
	if _, hasPidfile := tree[ServiceKeyPidfile]; hasPidfile && present {
		add("pidfile and pidfiles are mutually exclusive")
	}
	if !present {
		return
	}
	pidfiles, ok := raw.(map[string]any)
	if !ok {
		add(validationPidfilesMappingMsg)
		return
	}
	processes, _ := tree[sectionProcesses].(map[string]any)
	for _, role := range slices.Sorted(maps.Keys(pidfiles)) {
		path := pidfilesRolePath(role)
		if !validDocumentName(role) {
			add("%s role must be a simple name without path separators", path)
			continue
		}
		paths := cfgval.StringList(pidfiles[role])
		if len(paths) == 0 {
			add(validationNonEmptyPathListFormat, path)
			continue
		}
		for _, path := range paths {
			if !filepath.IsAbs(path) {
				add("%s path %q must be absolute", pidfilesRolePath(role), path)
			}
		}
		entry, ok := processes[role].(map[string]any)
		if !ok {
			add("%s requires matching %s", path, processEntryPath(role))
			continue
		}
		if cfgval.String(entry[process.SelectorKeyExe]) == "" {
			add("%s requires %s for exact pidfile identity", path, processFieldPath(role, process.SelectorKeyExe))
		}
		if cfgval.String(entry[process.SelectorKeyUser]) == "" {
			add("%s requires %s for exact pidfile identity", path, processFieldPath(role, process.SelectorKeyUser))
		}
	}
}

func validatePolicyExtras(tree map[string]any, add addFunc) {
	policy, ok := tree[sectionPolicy].(map[string]any)
	if !ok {
		return
	}
	if v, present := policy[rules.PolicyKeyMaxActions]; present {
		if n, ok := cfgval.Int(v); !ok || n <= 0 {
			add("%s must be an integer > 0", policyPathMaxActions)
		}
		if _, hasWindow := policy[rules.PolicyKeyMaxActionsWindow]; !hasWindow {
			add("%s requires %s", policyPathMaxActions, policyPathMaxActionsWindow)
		}
	}
	if v, present := policy[rules.PolicyKeyMaxActionsWindow]; present && !isPositiveDuration(cfgval.String(v)) {
		add("%s %q must be a valid positive duration", policyPathMaxActionsWindow, cfgval.String(v))
	}
	if bo, ok := policy[rules.PolicyKeyBackoff].(map[string]any); ok {
		initial := cfgval.String(bo[rules.BackoffKeyInitial])
		maxStr := cfgval.String(bo[rules.BackoffKeyMax])
		initialOK := isPositiveDuration(initial)
		maxOK := isPositiveDuration(maxStr)
		if !initialOK {
			add("%s must be a valid positive duration", policyPathBackoffInitial)
		}
		if !maxOK {
			add("%s must be a valid positive duration", policyPathBackoffMax)
		}
		// Only compare once both parse cleanly: otherwise a garbage initial
		// (di defaulting to 0) would let any max pass, and an omitted max would
		// report the misleading ">= initial" instead of its own parse error.
		if initialOK && maxOK {
			di, _ := time.ParseDuration(initial)
			dm, _ := time.ParseDuration(maxStr)
			if dm < di {
				add("%s must be >= initial", policyPathBackoffMax)
			}
		}
	}
}

func validateControl(tree map[string]any, add addFunc) {
	raw, present := tree[SectionControl]
	if !present {
		return
	}
	control, ok := raw.(map[string]any)
	if !ok {
		add("control must be a mapping")
		return
	}
	typ := cfgval.String(control[keyType])
	switch typ {
	case virt.ControlType:
		validateControlKeys(control, set(virt.ControlKeyType, virt.ControlKeyURI, virt.ControlKeyDomain, virt.ControlKeyUUID, virt.ControlKeySocket, virt.ControlKeyHost, virt.ControlKeyPort), libvirtControlKeySummary, add)
		validateLibvirtControl(control, add)
	case dockerctl.ControlType:
		validateControlKeys(control, set(dockerctl.ControlKeyType, dockerctl.ControlKeySocket, dockerctl.ControlKeyHost, dockerctl.ControlKeyPort, dockerctl.ControlKeyTLS, dockerctl.ControlKeyContainer), dockerControlKeySummary, add)
		validateDockerControl(control, add)
	default:
		add("%s %q is not one of %s", controlPathType, typ, controlTypeSummary)
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
	if domain := cfgval.String(control[virt.ControlKeyDomain]); domain == "" {
		add("%s is required for libvirt", controlPathDomain)
	}
	if uri := cfgval.String(control[virt.ControlKeyURI]); uri != "" && strings.TrimSpace(uri) == "" {
		add("%s must not be blank", controlPathURI)
	}
	if uuid := cfgval.String(control[virt.ControlKeyUUID]); uuid != "" {
		if _, err := virt.ParseUUID(uuid); err != nil {
			add("%s %q must be a canonical UUID or 32 hex digits", controlPathUUID, uuid)
		}
	}
	if socket := cfgval.String(control[virt.ControlKeySocket]); socket != "" && !virt.ValidSocketPath(socket) {
		add("%s %q must be an absolute path", controlPathSocket, socket)
	}
	host := cfgval.String(control[virt.ControlKeyHost])
	if host != "" && strings.TrimSpace(host) == "" {
		add("%s must not be blank", controlPathHost)
	}
	if host != "" && cfgval.String(control[virt.ControlKeySocket]) != "" {
		add(controlSocketHostConflictMessage)
	}
	if _, present := control[virt.ControlKeyPort]; present {
		port, ok := cfgval.Int(control[virt.ControlKeyPort])
		if !ok || !virt.ValidHostPort(host, port) {
			add(validationTCPPortRangeFormat, controlPathPort, cfgval.TCPPortRange())
		}
	}
}

func validateDockerControl(control map[string]any, add addFunc) {
	if container := cfgval.String(control[dockerctl.ControlKeyContainer]); container == "" {
		add("%s is required for docker", controlPathContainer)
	}
	if socket := cfgval.String(control[dockerctl.ControlKeySocket]); socket != "" && !filepath.IsAbs(socket) {
		add("%s %q must be an absolute path", controlPathSocket, socket)
	}
	host := cfgval.String(control[dockerctl.ControlKeyHost])
	if host != "" && strings.TrimSpace(host) == "" {
		add("%s must not be blank", controlPathHost)
	}
	if host != "" && cfgval.String(control[dockerctl.ControlKeySocket]) != "" {
		add(controlSocketHostConflictMessage)
	}
	if _, present := control[dockerctl.ControlKeyPort]; present {
		port, ok := cfgval.Int(control[dockerctl.ControlKeyPort])
		if !ok || !validTCPPort(port) {
			add(validationTCPPortRangeFormat, controlPathPort, cfgval.TCPPortRange())
		}
	}
	if !dockerctl.ValidTLSValue(control[dockerctl.ControlKeyTLS]) {
		add("%s %q is not one of %s", controlPathTLS, cfgval.String(control[dockerctl.ControlKeyTLS]), dockerctl.TLSValueSummary)
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
	raw, present := tree[SectionReload]
	if !present {
		return
	}
	r, ok := raw.(map[string]any)
	if !ok {
		add("reload must be a mapping with a signal or command")
		return
	}
	if when := cfgval.AsString(r[ReloadKeyWhen]); when != "" && when != ReloadWhenAuto && when != ReloadWhenAlways {
		add("%s %q must be %s", reloadPathWhen, when, ReloadWhenSummary)
	}
	sig := cfgval.AsString(r[ReloadKeySignal])
	_, hasCmd := r[ReloadKeyCommand]
	switch {
	case sig != "" && hasCmd:
		add("reload sets both signal and command; use exactly one")
	case sig != "":
		if _, err := process.ParseSignal(sig); err != nil {
			add("%s %q is not a known signal name (%s)", reloadPathSignal, sig, strings.Join(process.SignalNames(), ", "))
		} else if reloadSignalNeedsPidfileIdentity(tree, backend) {
			pidfile, identity := reloadSignalPidfileIdentity(tree)
			if !pidfile {
				add("%s requires top-level pidfile: when the service runs on OpenRC (no MainPID)", reloadPathSignal)
			}
			if !identity {
				add("%s requires a processes selector with both exe and user so the pidfile PID can be verified before signaling", reloadPathSignal)
			}
		}
	case hasCmd:
		if !cfgval.IsNonEmptyStringArray(r[ReloadKeyCommand]) {
			add("%s must be an array, not a shell string", reloadPathCommand)
		} else if len(cfgval.StringArray(r[ReloadKeyCommand])) == 0 {
			add("%s must not be empty", reloadPathCommand)
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
	svc, ok := tree[ServiceKeyService].(map[string]any)
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
	pidfile = len(cfgval.StringList(tree[ServiceKeyPidfile])) > 0
	procs, ok := tree[SectionProcesses].(map[string]any)
	if !ok {
		return pidfile, false
	}
	for _, raw := range procs {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if cfgval.String(entry[process.SelectorKeyExe]) != "" && cfgval.String(entry[process.SelectorKeyUser]) != "" {
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
	commands, ok := tree[sectionCommands].(map[string]any)
	if !ok {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(commands)) {
		path := sectionCommands + "." + name
		entry, ok := commands[name].(map[string]any)
		if !ok {
			add("%s must be a mapping", path)
			continue
		}
		validateCommandFields(path, entry, false, add)
		if v, present := entry[checks.CheckKeyTimeout]; present && !isPositiveDuration(cfgval.String(v)) {
			add("%s timeout %q must be a valid positive duration", path, cfgval.String(v))
		}
	}
}

// validateServiceField checks the `service` field: a scalar unit name or a
// per-init map of systemd/openrc candidate lists.
func validateServiceField(tree map[string]any, add addFunc) {
	s, present := tree[ServiceKeyService]
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
					add("%s must be a non-empty list", serviceBackendPath(k))
				}
			default:
				add("service key %q is not one of %s", k, initBackendSummary)
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
	a, present := tree[ServiceKeyAlsoService]
	if !present {
		return
	}
	m, ok := a.(map[string]any)
	if !ok {
		add("also_service must be a per-init map (systemd/openrc)")
		return
	}
	svc, _ := tree[ServiceKeyService].(map[string]any)
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if k != backendSystemd && k != backendOpenRC {
			add("also_service key %q is not one of %s", k, initBackendSummary)
			continue
		}
		units := cfgval.StringList(m[k])
		if !cfgval.IsNonEmptyStringArray(m[k]) {
			add("%s must be a non-empty list", alsoServiceBackendPath(k))
		}
		primary := map[string]bool{}
		if svc != nil {
			for _, u := range cfgval.StringList(svc[k]) {
				primary[u] = true
			}
		}
		for _, u := range units {
			if strings.TrimSpace(u) == "" {
				add("%s contains an empty unit", alsoServiceBackendPath(k))
			}
			if primary[u] {
				add("%s lists %q, which is the primary service unit", alsoServiceBackendPath(k), u)
			}
		}
	}
}

// validateCascade checks `also_apply`: each entry must be a known service and not
// the service itself. Targets receive the same action via their own operation.
func validateCascade(name string, tree map[string]any, services map[string]struct{}, add addFunc) {
	targets, err := cfgval.StrictStringList(tree[ServiceKeyAlsoApply])
	if _, present := tree[ServiceKeyAlsoApply]; present && err != nil {
		add(validationStringListFormat, ServiceKeyAlsoApply)
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
