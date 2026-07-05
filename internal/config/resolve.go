package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/rules"
)

// Resolved is a fully flattened, variable-expanded service definition.
type Resolved struct {
	Name string
	Tree map[string]any
}

// Resolve flattens a single service: it applies the defaults -> uses/clone ->
// overrides precedence, then expands ${var} references once. The
// returned errors include undefined-variable and nested-variable problems; a
// nil error slice means a clean resolution.
func (c *Config) Resolve(name string) (Resolved, []string) {
	return c.resolveService(name, true)
}

func (c *Config) resolveService(name string, pruneOptional bool) (Resolved, []string) {
	canonicalName, ok := c.CanonicalServiceName(name)
	if !ok {
		return Resolved{Name: name}, []string{fmt.Sprintf("unknown service %q", name)}
	}
	merged, err := c.mergedService(canonicalName, nil)
	if err != nil {
		return Resolved{Name: name}, []string{err.Error()}
	}
	if pruneOptional {
		merged = pruneEnableIf(merged, nil).(map[string]any)
	}

	errs := prepareExpansionInputs(merged)
	vars, varErrs := c.expansionVariables(merged, canonicalName)
	errs = append(errs, varErrs...)
	expanded, expErrs := expandTree(merged, vars)
	errs = append(errs, expErrs...)
	errs = append(errs, c.expandRestartOnChange(expanded)...)
	errs = append(errs, c.resolveChangedLibraries(expanded)...)
	errs = append(errs, expandReloadOnChange(expanded)...)
	errs = append(errs, c.expandApps(expanded)...)
	errs = append(errs, c.expandAnalyze(expanded)...)
	errs = append(errs, expandPidfile(expanded)...)
	errs = append(errs, expandPidfiles(expanded)...)
	errs = append(errs, expandSocket(expanded)...)
	errs = append(errs, expandLockfile(expanded)...)
	errs = append(errs, expandServiceWatches(expanded)...)

	return Resolved{Name: canonicalName, Tree: expanded}, errs
}

// ResolveStorage expands one configured storage document. Storage targets only
// merge the storage-safe subset of global defaults; each file under
// paths.storages is otherwise the complete declaration for one storage target
// and its optional fstab-backed mount operations.
func (c *Config) ResolveStorage(name string) (Resolved, []string) {
	doc, ok := c.Storages[name]
	if !ok {
		return Resolved{Name: name}, []string{fmt.Sprintf("unknown storage %q", name)}
	}
	body := mergeMaps(c.defaultsPerStorage(), stripMeta(doc.Body))
	vars, errs := c.expansionVariables(body, name)
	expanded, expErrs := expandTree(body, vars)
	errs = append(errs, expErrs...)
	return Resolved{Name: name, Tree: expanded}, errs
}

// StorageNameByPath returns the configured storage name whose resolved path
// matches path. Empty means no configured storage currently owns that path.
func (c *Config) StorageNameByPath(path string) string {
	cleanPath := cleanMountPath(path)
	for _, name := range c.StorageNames {
		resolved, errs := c.ResolveStorage(name)
		if len(errs) > 0 {
			continue
		}
		if cleanMountPath(cfgval.String(resolved.Tree["path"])) == cleanPath {
			return name
		}
	}
	return ""
}

func cleanMountPath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

// StorageMountNames returns the storage targets that expose mount operations.
func (c *Config) StorageMountNames() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.StorageNames))
	for _, name := range c.StorageNames {
		resolved, errs := c.ResolveStorage(name)
		if len(errs) > 0 {
			continue
		}
		if _, ok := resolved.Tree["mount"].(map[string]any); ok {
			out = append(out, name)
		}
	}
	return out
}

// expandPidfile validates a top-level `pidfile: <path>` or candidate list and
// adds a gated `pidfile` health check. The top-level declaration remains in the
// resolved tree as the service's single pidfile source; process discovery and
// OpenRC signal reload derive their internal pidfile selector from it.
func expandPidfile(tree map[string]any) []string {
	raw, present := tree["pidfile"]
	if !present {
		return nil
	}
	pathRaw := raw
	optional := false
	if m, ok := raw.(map[string]any); ok {
		pathRaw = m["path"]
		optional = cfgval.Bool(m["optional"])
	}
	paths := cfgval.StringList(pathRaw)
	if len(paths) == 0 {
		return []string{"pidfile must be a non-empty path string, list or {path: ...} mapping"}
	}
	var errs []string
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			errs = append(errs, fmt.Sprintf("pidfile path %q must be absolute", path))
		}
	}
	pathValue := pidfilePathValue(paths)
	tree["pidfile"] = pathValue

	// Gated health check, unless the service already defines one.
	checksMap, _ := tree["checks"].(map[string]any)
	if checksMap == nil {
		checksMap = map[string]any{}
	}
	if _, exists := checksMap["pidfile"]; !exists {
		entry := map[string]any{
			"type":     "pidfile",
			"path":     pathValue,
			"requires": []any{"service"},
		}
		if optional {
			entry["optional"] = true
		}
		checksMap["pidfile"] = entry
	}
	tree["checks"] = checksMap
	return errs
}

// expandPidfiles validates `pidfiles: {role: path-or-candidates}` and adds one
// gated pidfile health check per role. Unlike `pidfile: [...]`, whose list is a
// set of alternative paths for one process, `pidfiles` declares several process
// roles that must each have a live pidfile while the service is active.
func expandPidfiles(tree map[string]any) []string {
	raw, present := tree["pidfiles"]
	if !present {
		return nil
	}
	var errs []string
	if _, hasPidfile := tree["pidfile"]; hasPidfile {
		errs = append(errs, "pidfile and pidfiles are mutually exclusive")
	}
	pidfiles, ok := raw.(map[string]any)
	if !ok {
		return append(errs, "pidfiles must be a mapping of process role to path string or candidate list")
	}

	normalized := make(map[string]any, len(pidfiles))
	checksMap, _ := tree["checks"].(map[string]any)
	if checksMap == nil {
		checksMap = map[string]any{}
	}
	for _, role := range slices.Sorted(maps.Keys(pidfiles)) {
		if !validDocumentName(role) {
			errs = append(errs, fmt.Sprintf("pidfiles.%s role must be a simple name without path separators", role))
			continue
		}
		paths := cfgval.StringList(pidfiles[role])
		if len(paths) == 0 {
			errs = append(errs, fmt.Sprintf("pidfiles.%s must be a non-empty path string or list", role))
			continue
		}
		for _, path := range paths {
			if !filepath.IsAbs(path) {
				errs = append(errs, fmt.Sprintf("pidfiles.%s path %q must be absolute", role, path))
			}
		}
		pathValue := pidfilePathValue(paths)
		normalized[role] = pathValue
		checkName := "pidfile-" + role
		if _, exists := checksMap[checkName]; !exists {
			checksMap[checkName] = map[string]any{
				"type":     "pidfile",
				"path":     pathValue,
				"requires": []any{"service"},
			}
		}
	}
	tree["pidfiles"] = normalized
	tree["checks"] = checksMap
	return errs
}

// expandSocket desugars a top-level `socket:` declaration into a gated health
// check. A service-created runtime socket should not block start/restart
// preflight: it is checked while the service is active, like pidfiles.
func expandSocket(tree map[string]any) []string {
	raw, present := tree["socket"]
	if !present {
		return nil
	}
	delete(tree, "socket")

	pathRaw := raw
	optional := false
	if m, ok := raw.(map[string]any); ok {
		pathRaw = m["path"]
		optional = cfgval.Bool(m["optional"])
	}
	paths := cfgval.StringList(pathRaw)
	if len(paths) == 0 {
		return []string{"socket must be a non-empty path string, list or {path: ...} mapping"}
	}
	var errs []string
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			errs = append(errs, fmt.Sprintf("socket path %q must be absolute", path))
		}
	}

	checksMap, _ := tree["checks"].(map[string]any)
	if checksMap == nil {
		checksMap = map[string]any{}
	}
	if _, exists := checksMap["socket"]; !exists {
		entry := map[string]any{
			"type":     "socket",
			"path":     pidfilePathValue(paths),
			"requires": []any{"service"},
		}
		if optional {
			entry["optional"] = true
		}
		checksMap["socket"] = entry
	}
	tree["checks"] = checksMap
	return errs
}

// expandLockfile desugars a top-level `lockfile:` declaration into a gated
// health check. It is for service-owned runtime lock artifacts, not Sermo
// operation locks.
func expandLockfile(tree map[string]any) []string {
	raw, present := tree["lockfile"]
	if !present {
		return nil
	}
	delete(tree, "lockfile")

	pathRaw := raw
	optional := false
	if m, ok := raw.(map[string]any); ok {
		pathRaw = m["path"]
		optional = cfgval.Bool(m["optional"])
	}
	paths := cfgval.StringList(pathRaw)
	if len(paths) == 0 {
		return []string{"lockfile must be a non-empty path string, list or {path: ...} mapping"}
	}
	var errs []string
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			errs = append(errs, fmt.Sprintf("lockfile path %q must be absolute", path))
		}
	}

	checksMap, _ := tree["checks"].(map[string]any)
	if checksMap == nil {
		checksMap = map[string]any{}
	}
	if _, exists := checksMap["lockfile"]; !exists {
		entry := map[string]any{
			"type":     "lockfile",
			"path":     pidfilePathValue(paths),
			"requires": []any{"service"},
		}
		if optional {
			entry["optional"] = true
		}
		checksMap["lockfile"] = entry
	}
	tree["checks"] = checksMap
	return errs
}

func pidfilePathValue(paths []string) any {
	if len(paths) == 1 {
		return paths[0]
	}
	out := make([]any, 0, len(paths))
	for _, path := range paths {
		out = append(out, path)
	}
	return out
}

// expandAnalyze resolves each check's `analyze` block into the flat rule list the
// checks package consumes: it concatenates the `rules` of every `use:` pattern
// set (in order), drops any rule whose id is in `silence:`, then appends the
// check's local `rules:`, and replaces the block with `{rules: [...]}`. An
// unknown set name, a `silence` id absent from the inherited sets, or a duplicate
// id in the resolved list is an error. Checks without `analyze` are untouched.
func (c *Config) expandAnalyze(tree map[string]any) []string {
	checks, ok := tree["checks"].(map[string]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []string
	for _, name := range names {
		entry, ok := checks[name].(map[string]any)
		if !ok {
			continue
		}
		analyze, ok := entry["analyze"].(map[string]any)
		if !ok {
			if _, present := entry["analyze"]; present {
				errs = append(errs, "checks."+name+".analyze must be a mapping")
			}
			continue
		}
		rules, rerrs := c.resolveAnalyze(name, analyze)
		errs = append(errs, rerrs...)
		entry["analyze"] = map[string]any{"rules": rules}
	}
	return errs
}

// resolveAnalyze builds the flat, ordered rule list for one check's analyze block.
func (c *Config) resolveAnalyze(checkName string, analyze map[string]any) ([]any, []string) {
	var errs []string
	scope := "checks." + checkName + ".analyze"

	silence := map[string]bool{}
	for _, id := range cfgval.StringList(analyze["silence"]) {
		silence[id] = true
	}
	seenSilence := map[string]bool{}

	var rules []any
	ids := map[string]bool{}
	addRule := func(r any) {
		rm, ok := r.(map[string]any)
		if !ok {
			errs = append(errs, scope+": each rule must be a mapping")
			return
		}
		id := cfgval.AsString(rm["id"])
		if id != "" && ids[id] {
			errs = append(errs, fmt.Sprintf("%s: duplicate rule id %q", scope, id))
			return
		}
		ids[id] = true
		rules = append(rules, r)
	}

	// Local rules come FIRST so the service takes precedence: a local rule (e.g.
	// an `ok` whitelist for a known-benign line) wins over an inherited rule that
	// would otherwise match the same line, since evaluation is first-match-wins.
	if local, ok := analyze["rules"].([]any); ok {
		for _, r := range local {
			addRule(r)
		}
	}

	// Inherited rules from each `use` set, in order, minus silenced ids.
	for _, setName := range cfgval.StringList(analyze["use"]) {
		doc, ok := c.Patterns[setName]
		if !ok {
			errs = append(errs, fmt.Sprintf("%s.use references %q, which is not a patterns set", scope, setName))
			continue
		}
		setRules, _ := doc.Body["rules"].([]any)
		for _, r := range setRules {
			if rm, ok := r.(map[string]any); ok {
				if id := cfgval.AsString(rm["id"]); id != "" && silence[id] {
					seenSilence[id] = true
					continue
				}
			}
			addRule(r)
		}
	}

	// A silence id that matched no inherited rule is a typo worth catching.
	for _, id := range cfgval.StringList(analyze["silence"]) {
		if !seenSilence[id] {
			errs = append(errs, fmt.Sprintf("%s.silence references id %q not present in the inherited sets", scope, id))
		}
	}

	return rules, errs
}

// expandReloadOnChange desugars a `reload_on_change: {paths: [...]}` block into
// one remediation rule per path that *reloads* the service (re-reads its config
// in place, no restart) when that file changes. It is the non-disruptive analog
// of restart_on_change, for catalog services whose config can be reloaded (udev rules,
// nginx vhosts, named zones, …). The block is removed; an empty paths list is a
// no-op.
func expandReloadOnChange(tree map[string]any) []string {
	roc, ok := tree["reload_on_change"].(map[string]any)
	if !ok {
		if _, present := tree["reload_on_change"]; present {
			delete(tree, "reload_on_change")
			return []string{"reload_on_change must be a mapping with a paths list"}
		}
		return nil
	}
	delete(tree, "reload_on_change")

	rules, _ := tree["rules"].(map[string]any)
	if rules == nil {
		rules = map[string]any{}
	}
	var errs []string
	for i, p := range cfgval.StringList(roc["paths"]) {
		if p == "" {
			errs = append(errs, "reload_on_change.paths entry is empty")
			continue
		}
		key := fmt.Sprintf("reload-on-change-%d", i+1)
		if _, exists := rules[key]; exists {
			errs = append(errs, fmt.Sprintf("reload_on_change would overwrite existing rule %q; rename that rule", key))
			continue
		}
		rules[key] = map[string]any{
			"type": "remediation",
			"if":   map[string]any{"changed": map[string]any{"path": p}},
			"then": map[string]any{"action": "reload"},
		}
	}
	if len(rules) > 0 {
		tree["rules"] = rules
	}
	return errs
}

// expandServiceWatches desugars unified service watches whose `then` declares a
// rule-class action (restart/start/stop/reload/resume → remediation, block → guard,
// alert → alert) into a generated `checks:` probe plus the equivalent `rules:` entry,
// then removes them from `watches:`. What remains under `watches:` is only the
// fire-and-forget entries (hook/notify/expand/kill), built by the Watch runtime.
//
// The generated check embeds the watch's `check:` block verbatim (accepting probe
// duplication when two watches share an endpoint) and carries verify/requires/
// optional/interval when present. The rule's condition polarity follows the check
// type: a health check fires on failure (`failed`), a condition check on its
// threshold (`active`), matching checks.IsHealthType. The check and rule take the
// watch's name; a collision with an existing check/rule is an error.
func expandServiceWatches(tree map[string]any) []string {
	watches, ok := tree["watches"].(map[string]any)
	if !ok {
		return nil
	}
	checksMap, _ := tree["checks"].(map[string]any)
	if checksMap == nil {
		checksMap = map[string]any{}
	}
	rulesMap, _ := tree["rules"].(map[string]any)
	if rulesMap == nil {
		rulesMap = map[string]any{}
	}
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	for _, name := range slices.Sorted(maps.Keys(watches)) {
		entry, ok := watches[name].(map[string]any)
		if !ok {
			continue
		}
		then, _ := entry["then"].(map[string]any)
		action := cfgval.String(then["action"])
		if action == "" || !isRuleClassAction(action) {
			continue // fire-and-forget watch (or invalid action): left for validateServiceWatches
		}
		// Validate the action grammar here (this entry is removed before the
		// resolved-tree validators run, so they never see it).
		validateWatchThenAction("watches."+name, action, then, add)
		check, ok := entry["check"].(map[string]any)
		if !ok {
			add("watches.%s.check is required", name)
			continue
		}
		if _, exists := rulesMap[name]; exists {
			add("watches.%s would overwrite existing rule %q; rename the watch", name, name)
			continue
		}

		// The rule references either an existing named check (`check: {ref: name}`,
		// so a shared health/verify probe is not duplicated) or a probe embedded in
		// the watch, generated as a check named after the watch.
		var refName, refType string
		if ref := cfgval.String(check["ref"]); ref != "" {
			refName, refType = ref, lookupCheckType(tree, ref)
			if refType == "" {
				add("watches.%s.check.ref %q does not name a checks: or preflight: entry", name, ref)
				continue
			}
			// Entry-level check fields belong on the referenced check, not the
			// referencing watch — reject them rather than drop them silently.
			for _, k := range []string{"verify", "requires", "optional", "interval"} {
				if _, has := entry[k]; has {
					add("watches.%s.%s is not supported with check.ref; set it on the referenced check %q", name, k, ref)
				}
			}
		} else {
			if _, exists := checksMap[name]; exists {
				add("watches.%s would overwrite existing check %q; rename the watch", name, name)
				continue
			}
			genCheck := cloneMap(check)
			for _, k := range []string{"verify", "requires", "optional", "interval"} {
				if v, has := entry[k]; has {
					genCheck[k] = v
				}
			}
			checksMap[name] = genCheck
			refName, refType = name, cfgval.String(check["type"])
		}

		// Condition polarity from the check type: health fires on failure, a
		// condition (metric/storage/…) on its threshold.
		operand := map[string]any{"check": refName}
		var cond map[string]any
		if checks.IsHealthType(refType) {
			cond = map[string]any{"failed": operand}
		} else {
			cond = map[string]any{"active": operand}
		}

		// 3. generated rule.
		rule := map[string]any{"if": cond}
		if w, has := entry["for"]; has {
			rule["for"] = w
		}
		if w, has := entry["within"]; has {
			rule["within"] = w
		}
		thenOut := map[string]any{"action": action}
		if msg := cfgval.String(then["message"]); msg != "" {
			thenOut["message"] = msg
		}
		switch rules.ActionType(action) {
		case rules.ActionBlock:
			rule["type"] = string(rules.RuleGuard)
			if b := cfgval.StringList(then["blocks"]); len(b) > 0 {
				rule["blocks"] = then["blocks"]
			}
		case rules.ActionAlert:
			rule["type"] = string(rules.RuleAlert)
		default:
			rule["type"] = string(rules.RuleRemediation)
		}
		// A rule's notify is an entry-level field (ParseRules reads entry["notify"]),
		// not part of then; a guard never notifies.
		if action != string(rules.ActionBlock) {
			if n, has := then["notify"]; has {
				rule["notify"] = n
			}
		}
		rule["then"] = thenOut
		rulesMap[name] = rule

		delete(watches, name)
	}

	if len(checksMap) > 0 {
		tree["checks"] = checksMap
	}
	if len(rulesMap) > 0 {
		tree["rules"] = rulesMap
	}
	if len(watches) == 0 {
		delete(tree, "watches")
	}
	return errs
}

// lookupCheckType returns the type of a named check in the checks: or preflight:
// section, or "" when no such check exists. Used to resolve the polarity of a
// watch that references a shared check by name (check: {ref: …}).
func lookupCheckType(tree map[string]any, name string) string {
	for _, section := range []string{"checks", "preflight"} {
		if m, ok := tree[section].(map[string]any); ok {
			if c, ok := m[name].(map[string]any); ok {
				return cfgval.String(c["type"])
			}
		}
	}
	return ""
}

// injectBuiltinVariables makes the document's identity available for ${...}
// expansion: ${name} (the resolved service name), ${display_name} (the
// display_name field, falling back to name), ${service} (the primary unit),
// ${host} (the detected hostname), ${hostname} (the short hostname, for
// host-keyed systemd instance units such as ceph-mon@${hostname}), ${init} (the
// detected init system), ${user} (the Sermo user, a fallback for service
// accounts), ${pidfile} (the conventional /run/<unit>.pid) and ${port} (the
// top-level `port:` field, when set). They let catalog services parameterize strings — e.g. a tcp check
// port: "${port}" or message: "${display_name} backup is running".
// Injected after validateVariableValues so a display_name carrying its own
// ${...} is not mistaken for a nested variable; an explicit `variables` entry of
// the same name takes precedence and is left untouched.
func injectBuiltinVariables(vars map[string]string, name string, merged map[string]any) {
	if _, ok := vars["name"]; !ok {
		vars["name"] = name
	}
	if _, ok := vars["display_name"]; !ok {
		vars["display_name"] = DisplayName(merged, name)
	}
	if _, ok := vars["service"]; !ok {
		vars["service"] = ServiceUnit(merged, name)
	}
	injectHostBuiltins(vars)
	// ${pidfile} falls back to the conventional /run/<unit>.pid; an explicit
	// `pidfile` variable always wins.
	if _, ok := vars["pidfile"]; !ok {
		vars["pidfile"] = "/run/" + vars["service"] + ".pid"
	}
	// ${port} mirrors the top-level `port:` field; unlike the others it has no
	// fallback, so it is injected only when the field is set — leaving ${port}
	// undefined (and so a clear error) when nothing provides a port.
	if _, ok := vars["port"]; !ok {
		if p := cfgval.String(merged["port"]); p != "" {
			vars["port"] = p
		}
	}
}

// injectHostBuiltins fills the service-independent (host-level) builtins —
// host/hostname/init/user — when absent. Shared by injectBuiltinVariables and
// the watch expansion (watches have no service-specific builtins).
func injectHostBuiltins(vars map[string]string) {
	if _, ok := vars["host"]; !ok {
		vars["host"] = detectedHost
	}
	if _, ok := vars["hostname"]; !ok {
		vars["hostname"] = detectedHostname
	}
	if _, ok := vars["init"]; !ok {
		vars["init"] = detectedInit
	}
	if _, ok := vars["user"]; !ok {
		vars["user"] = detectedUser
	}
}

// globalVars returns the custom variables declared under `defaults.variables`,
// processed through collectVariables so they get the same env (${env:...}) and
// list-first-existing handling as per-service variables. They form the lowest
// explicit layer (a service's own variables override them; builtins fill gaps).
func (c *Config) globalVars() map[string]string {
	return collectVariables(map[string]any{"variables": c.Global.Defaults["variables"]})
}

func (c *Config) expansionVariables(tree map[string]any, name string) (map[string]string, []string) {
	return c.expansionVariablesForKind(tree, name, cfgval.String(tree["kind"]))
}

func (c *Config) expansionVariablesForKind(tree map[string]any, name string, kind string) (map[string]string, []string) {
	vars := c.globalVars()
	appVars, errs := c.appVariables(tree)
	maps.Copy(vars, appVars)
	maps.Copy(vars, collectVariablesForKind(tree, kind)) // service/doc variables override app and global custom ones
	errs = append(errs, validateVariableValues(vars)...)
	injectBuiltinVariables(vars, name, tree)
	errs = append(errs, resolveFileVars(vars, tree)...)
	return vars, errs
}

func (c *Config) appVariables(tree map[string]any) (map[string]string, []string) {
	names := cfgval.StringList(tree["apps"])
	if len(names) == 0 {
		return nil, nil
	}

	var errs []string
	out := map[string]string{}
	source := map[string]string{}
	exposeDefaults := len(names) == 1
	for _, name := range names {
		doc, ok := c.Apps[name]
		if !ok {
			continue // expandApps reports the missing app in the usual place.
		}
		body := stripMeta(doc.Body)
		errs = append(errs, prepareExpansionInputs(body)...)
		appVars := collectVariablesForKind(body, doc.Kind)
		errs = append(errs, resolveFileVars(appVars, body)...)
		// Iterate variable names in sorted order so conflict errors surface in a
		// stable, reproducible order (map ranging is randomized).
		varNames := slices.Sorted(maps.Keys(appVars))
		if exposeDefaults {
			for _, varName := range varNames {
				errs = append(errs, addAppVariable(out, source, varName, name, appVars[varName])...)
			}
		}
		prefixes := []string{appVariablePrefix(name)}
		if doc.Name != name {
			prefixes = append(prefixes, appVariablePrefix(doc.Name))
		}
		for _, varName := range varNames {
			value := appVars[varName]
			for _, prefix := range prefixes {
				key := appVariableKey(prefix, varName)
				errs = append(errs, addAppVariable(out, source, key, name, value)...)
			}
		}
	}
	return out, errs
}

func addAppVariable(out, source map[string]string, key, appName, value string) []string {
	if key == "" {
		return nil
	}
	if prev, exists := out[key]; exists && prev != value {
		return []string{fmt.Sprintf("apps variable ${%s} from app %q conflicts with app %q", key, appName, source[key])}
	}
	out[key] = value
	source[key] = appName
	return nil
}

func appVariableKey(prefix, name string) string {
	suffix := appVariablePrefix(name)
	if prefix == "" || suffix == "" {
		return ""
	}
	return prefix + "_" + suffix
}

func appVariablePrefix(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		switch {
		case r == '_':
			if b.Len() > 0 && !lastUnderscore {
				b.WriteRune('_')
				lastUnderscore = true
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			lastUnderscore = false
		default:
			if b.Len() > 0 && !lastUnderscore {
				b.WriteRune('_')
				lastUnderscore = true
			}
		}
	}
	return strings.TrimRight(b.String(), "_")
}

// ResolveWatches returns the global `watches` section plus storage capacity
// watches with ${var} expanded against the custom global variables and the
// host-level builtins. Watches have no per-watch builtins (name/port/pidfile).
// nil when no watches are configured.
func (c *Config) ResolveWatches() (map[string]any, []string) {
	raw := map[string]any{}
	if configured, ok := c.Global.Raw["watches"].(map[string]any); ok {
		for name, entry := range configured {
			raw[name] = deepCopy(entry)
		}
	}
	errs := c.addStorageCapacityWatches(raw)
	if len(raw) == 0 {
		return nil, errs
	}
	c.applyWatchDefaults(raw)
	vars := c.globalVars()
	injectHostBuiltins(vars)
	expanded, expErrs := expandTree(raw, vars)
	errs = append(errs, expErrs...)
	return expanded, errs
}

func (c *Config) addStorageCapacityWatches(dst map[string]any) []string {
	if c == nil {
		return nil
	}
	var errs []string
	for _, name := range c.StorageNames {
		resolved, resErrs := c.ResolveStorage(name)
		for _, err := range resErrs {
			errs = append(errs, "storage "+name+": "+err)
		}
		if len(resErrs) > 0 || resolved.Tree == nil {
			continue
		}
		entry, ok := storageCapacityWatch(resolved.Tree)
		if !ok {
			continue
		}
		if _, exists := dst[name]; exists {
			errs = append(errs, fmt.Sprintf("storage %q capacity watch would overwrite existing watch %q", name, name))
			continue
		}
		dst[name] = entry
	}
	return errs
}

func storageCapacityWatch(tree map[string]any) (map[string]any, bool) {
	capacity, ok := tree["capacity"].(map[string]any)
	if !ok {
		return nil, false
	}
	check := map[string]any{
		"type": "storage",
		"path": tree["path"],
	}
	if _, hasMount := tree["mount"].(map[string]any); hasMount {
		if _, explicit := capacity["mounted"]; !explicit {
			check["mounted"] = true
		}
	}
	for _, key := range append([]string{"mounted"}, checks.StoragePredFields...) {
		if v, present := capacity[key]; present {
			check[key] = v
		}
	}
	entry := map[string]any{"check": check}
	for _, key := range []string{"display_name", "description", "category", "dry_run", "monitor", "interval"} {
		if v, present := tree[key]; present {
			entry[key] = v
		}
	}
	for _, key := range []string{"for", "within", "then", "policy"} {
		if v, present := capacity[key]; present {
			entry[key] = v
		}
	}
	return entry, true
}

// expandRestartOnChange desugars a `restart_on_change: {libraries: [...]}` block
// into one remediation rule per library that restarts the service when the
// library file changes. Each named library is resolved to its file via the
// matching library, so the generated `changed:` condition carries a
// concrete path. The block is removed; unknown or non-library references error.
func (c *Config) expandRestartOnChange(tree map[string]any) []string {
	roc, ok := tree["restart_on_change"].(map[string]any)
	if !ok {
		return nil
	}
	delete(tree, "restart_on_change")

	var errs []string
	libraries, _ := tree["rules"].(map[string]any)
	if libraries == nil {
		libraries = map[string]any{}
	}
	for _, lib := range cfgval.StringList(roc["libraries"]) {
		path, known := c.libraryPath(lib)
		switch {
		case !known:
			errs = append(errs, fmt.Sprintf("restart_on_change references %q, which is not a library", lib))
			continue
		case path == "":
			errs = append(errs, fmt.Sprintf("library %q has no binary to watch", lib))
			continue
		}
		key := "restart-on-change-" + lib
		if _, exists := libraries[key]; exists {
			errs = append(errs, fmt.Sprintf("restart_on_change would overwrite existing rule %q; rename that rule", key))
			continue
		}
		libraries[key] = map[string]any{
			"type": "remediation",
			"if":   map[string]any{"changed": map[string]any{"library": lib, "path": path}},
			"then": map[string]any{"action": "restart"},
		}
	}
	if len(libraries) > 0 {
		tree["rules"] = libraries
	}
	return errs
}

// libraryPath resolves a library name to the file its library watches
// (the `binary` variable). known is false when no library has that name; an
// empty path with known=true means the library declares no binary. Shared by
// expandRestartOnChange and the `changed: {library: X}` condition rewrite.
func (c *Config) libraryPath(lib string) (path string, known bool) {
	doc, ok := c.Libraries[lib]
	if !ok {
		return "", false
	}
	return DocumentBinary(doc.Body), true
}

// resolveChangedLibraries fills the `path` of a hand-written
// `changed: {library: X}` condition in every rule's if-tree, resolving the
// library exactly like restart_on_change does — so the documented shorthand
// works anywhere a condition does. Conditions already carrying a path are left
// untouched (the restart_on_change desugar emits both keys).
func (c *Config) resolveChangedLibraries(tree map[string]any) []string {
	rules, ok := tree["rules"].(map[string]any)
	if !ok {
		return nil
	}
	var errs []string
	for _, name := range slices.Sorted(maps.Keys(rules)) {
		rule, ok := rules[name].(map[string]any)
		if !ok {
			continue
		}
		if ifNode, ok := rule["if"].(map[string]any); ok {
			errs = append(errs, c.fillChangedLibraryPaths(ifNode, "rules."+name)...)
		}
	}
	return errs
}

// fillChangedLibraryPaths walks one condition node (recursing through and/or/
// not) and rewrites its changed-library leaf, collecting resolution errors.
func (c *Config) fillChangedLibraryPaths(node map[string]any, scope string) []string {
	var errs []string
	for _, key := range []string{"and", "or"} {
		items, ok := node[key].([]any)
		if !ok {
			continue
		}
		for _, item := range items {
			if child, ok := item.(map[string]any); ok {
				errs = append(errs, c.fillChangedLibraryPaths(child, scope)...)
			}
		}
	}
	if child, ok := node["not"].(map[string]any); ok {
		errs = append(errs, c.fillChangedLibraryPaths(child, scope)...)
	}
	ch, ok := node["changed"].(map[string]any)
	if !ok {
		return errs
	}
	lib := cfgval.String(ch["library"])
	if lib == "" || cfgval.String(ch["path"]) != "" {
		return errs
	}
	path, known := c.libraryPath(lib)
	switch {
	case !known:
		errs = append(errs, fmt.Sprintf("%s: changed references %q, which is not a library", scope, lib))
	case path == "":
		errs = append(errs, fmt.Sprintf("%s: library %q has no binary to watch", scope, lib))
	default:
		ch["path"] = path
	}
	return errs
}

// expandApps adds each app's binary/health/version preflight checks under
// namespaced keys (`<app>-<check>`). App preflight failures block
// start/restart/reload/resume. The `apps` key is consumed here.
func (c *Config) expandApps(tree map[string]any) []string {
	return c.expandAppsChain(tree, nil)
}

// expandAppsChain is expandApps with cycle tracking: chain carries the app names
// already being resolved on this path so a self- or mutually-referential
// `apps:` linkage (an app document that itself lists `apps:`) fails as a config
// error instead of recursing until the stack overflows. chain holds app names
// only — a catalog service/service that links an app of the same name is not a cycle.
func (c *Config) expandAppsChain(tree map[string]any, chain []string) []string {
	_, present := tree["apps"]
	names := cfgval.StringList(tree["apps"])
	delete(tree, "apps")
	if !present {
		return nil
	}

	var errs []string
	preflight, _ := tree["preflight"].(map[string]any)
	if preflight == nil {
		preflight = map[string]any{}
	}
	for _, name := range names {
		if slices.Contains(chain, name) {
			cycle := append(append([]string{}, chain...), name)
			errs = append(errs, fmt.Sprintf("apps cycle detected: %s", strings.Join(cycle, " -> ")))
			continue
		}
		doc, ok := c.Apps[name]
		if !ok {
			errs = append(errs, fmt.Sprintf("apps references %q, which is not an app", name))
			continue
		}
		resolved, rerrs := c.resolveDocBody(doc, name, append(append([]string{}, chain...), name))
		if len(rerrs) > 0 {
			errs = append(errs, rerrs...)
			continue
		}
		appPre, _ := resolved.Tree["preflight"].(map[string]any)
		for checkName, check := range appPre {
			key := fmt.Sprintf("%s-%s", name, checkName)
			if _, exists := preflight[key]; exists {
				errs = append(errs, fmt.Sprintf("apps preflight key %q would overwrite an existing preflight check; rename one of the checks", key))
				continue
			}
			if checkName == "version" {
				if match, present := resolved.Tree["version_match"]; present {
					if checkMap, ok := check.(map[string]any); ok {
						check = maps.Clone(checkMap)
						check.(map[string]any)["version_match"] = match
					}
				}
			}
			preflight[key] = check
		}
	}
	if len(preflight) > 0 {
		tree["preflight"] = preflight
	}
	return errs
}

// ResolveCatalogService expands a catalog service's own body — no service merge
// — so its concrete values (notably the binary path and preflight commands) can
// be inspected directly, as the `apps` command does. ${name} and ${display_name}
// are available; the returned errors mirror Resolve's.
func (c *Config) ResolveCatalogService(name string) (Resolved, []string) {
	canonicalName, ok := c.CanonicalCatalogName(CategoryService, name)
	if !ok {
		return Resolved{Name: name}, []string{fmt.Sprintf("unknown catalog service %q", name)}
	}
	doc, ok := c.CatalogServices[canonicalName]
	if !ok {
		return Resolved{Name: name}, []string{fmt.Sprintf("unknown catalog service %q", name)}
	}
	return c.resolveDoc(doc, canonicalName)
}

// catalogRegistry returns the registry that holds a given category's
// definitions (apps, libraries, patterns, else the catalog services).
func (c *Config) catalogRegistry(category string) map[string]*Document {
	switch category {
	case CategoryApp:
		return c.Apps
	case CategoryLibrary:
		return c.Libraries
	case CategoryPatterns:
		return c.Patterns
	default:
		return c.CatalogServices
	}
}

// ResolveCatalog expands a catalog definition from the registry for its category
// (service | app | library). It lets category-scoped listings (`apps`, `libs`,
// `services`) resolve a name in its own registry, since names may repeat across
// kinds.
func (c *Config) ResolveCatalog(category, name string) (Resolved, []string) {
	canonicalName, ok := c.CanonicalCatalogName(category, name)
	if !ok {
		return Resolved{Name: name}, []string{fmt.Sprintf("unknown %s %q", category, name)}
	}
	doc := c.catalogRegistry(category)[canonicalName]
	if doc == nil {
		return Resolved{Name: name}, []string{fmt.Sprintf("unknown %s %q", category, name)}
	}
	return c.resolveDoc(doc, canonicalName)
}

// resolveDoc expands a single catalog document's own body (no service merge),
// shared by ResolveCatalogService and the `apps` linkage (which resolves app documents).
func (c *Config) resolveDoc(doc *Document, name string) (Resolved, []string) {
	// Top level (catalog service / service): its apps: links start a fresh app
	// chain. The top-level name is a different namespace than apps, so a catalog service
	// linking an app of the same name is not a cycle.
	return c.resolveDocBody(doc, name, nil)
}

// resolveDocBody expands doc's own body and its apps: links, threading appChain
// (the app names already being resolved on this path) so expandAppsChain can
// detect a cyclic apps: linkage instead of recursing into a stack overflow.
func (c *Config) resolveDocBody(doc *Document, name string, appChain []string) (Resolved, []string) {
	body := stripMeta(doc.Body)
	body = pruneEnableIf(body, nil).(map[string]any)
	errs := prepareExpansionInputs(body)
	vars, varErrs := c.expansionVariablesForKind(body, name, doc.Kind)
	errs = append(errs, varErrs...)
	expanded, expErrs := expandTree(body, vars)
	errs = append(errs, expErrs...)
	errs = append(errs, c.expandAppsChain(expanded, appChain)...)
	errs = append(errs, c.expandAnalyze(expanded)...)
	errs = append(errs, expandPidfile(expanded)...)
	errs = append(errs, expandPidfiles(expanded)...)
	errs = append(errs, expandSocket(expanded)...)
	errs = append(errs, expandLockfile(expanded)...)
	errs = append(errs, expandServiceWatches(expanded)...)
	return Resolved{Name: name, Tree: expanded}, errs
}

// mergedService returns the merged-but-unexpanded body for a service, following
// its uses/clone layering. chain tracks the active clone path for cycle
// detection.
func (c *Config) mergedService(name string, chain []string) (map[string]any, error) {
	canonicalName, ok := c.CanonicalServiceName(name)
	if !ok {
		return nil, fmt.Errorf("unknown service %q", name)
	}
	name = canonicalName
	for _, prev := range chain {
		if prev == name {
			cycle := append(append([]string{}, chain...), name)
			return nil, fmt.Errorf("clone cycle detected: %s", strings.Join(cycle, " -> "))
		}
	}

	doc, ok := c.Services[name]
	if !ok {
		return nil, fmt.Errorf("unknown service %q", name)
	}

	var merged map[string]any
	if clone := cfgval.String(doc.Body["clone"]); clone != "" {
		// clone and uses are mutually exclusive: the clone branch ignores uses
		// entirely, so accepting both would silently drop the catalog service the author
		// asked to inherit. Surface it instead.
		if uses := cfgval.String(doc.Body["uses"]); uses != "" {
			return nil, fmt.Errorf("service %q sets both clone and uses, which are mutually exclusive", name)
		}
		src, err := c.mergedService(clone, append(chain, name))
		if err != nil {
			return nil, err
		}
		merged = src
	} else {
		merged = c.defaultsPerService()
		if uses := cfgval.String(doc.Body["uses"]); uses != "" {
			catalogName, ok := c.CanonicalCatalogName(CategoryService, uses)
			if !ok {
				return nil, fmt.Errorf("service %q uses unknown catalog service %q", name, uses)
			}
			base := c.CatalogServices[catalogName]
			merged = mergeMaps(merged, stripMeta(base.Body))
		}
	}

	merged = mergeMaps(merged, stripMeta(doc.Body))
	applyDeletes(merged)
	return merged, nil
}

// defaultsPerService returns a fresh copy of global defaults that apply to
// services.
func (c *Config) defaultsPerService() map[string]any {
	out := map[string]any{}
	for _, key := range perServiceDefaults {
		if v, ok := c.Global.Defaults[key]; ok {
			out[key] = deepCopy(v)
		}
	}
	return out
}

// defaultsPerStorage returns a fresh copy of global defaults that apply to
// storages.
func (c *Config) defaultsPerStorage() map[string]any {
	out := map[string]any{}
	for _, key := range perStorageDefaults {
		if v, ok := c.Global.Defaults[key]; ok {
			out[key] = deepCopy(v)
		}
	}
	return out
}

func (c *Config) applyWatchDefaults(raw map[string]any) {
	v, ok := c.Global.Defaults["dry_run"]
	if !ok {
		return
	}
	for _, entry := range raw {
		watch, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if _, present := watch["dry_run"]; !present {
			watch["dry_run"] = deepCopy(v)
		}
	}
}

// stripMeta returns a copy of a document body without the resolution-control
// keys (kind/name/uses/clone), which are not part of the merged service.
func stripMeta(body map[string]any) map[string]any {
	out := make(map[string]any, len(body))
	for k, v := range body {
		if _, meta := metaKeys[k]; meta {
			continue
		}
		out[k] = deepCopy(v)
	}
	return out
}
