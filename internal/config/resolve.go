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
)

// Resolved is a fully flattened, variable-expanded service definition.
type Resolved struct {
	Name string
	Tree map[string]any
}

// Resolve flattens a single service: it applies the defaults -> uses/clone ->
// overrides precedence (section 8), then expands ${var} references once. The
// returned errors include undefined-variable and nested-variable problems; a
// nil error slice means a clean resolution.
func (c *Config) Resolve(name string) (Resolved, []string) {
	merged, err := c.mergedService(name, nil)
	if err != nil {
		return Resolved{Name: name}, []string{err.Error()}
	}

	vars, errs := c.expansionVariables(merged, name)
	errs = append(errs, expandBinary(merged, cfgval.String(merged["kind"]))...)
	expanded, expErrs := expandTree(merged, vars)
	errs = append(errs, expErrs...)
	errs = append(errs, c.expandRestartOnChange(expanded)...)
	errs = append(errs, c.resolveChangedLibraries(expanded)...)
	errs = append(errs, expandReloadOnChange(expanded)...)
	errs = append(errs, c.expandApps(expanded)...)
	errs = append(errs, c.expandAnalyze(expanded)...)
	errs = append(errs, expandPidfile(expanded)...)

	return Resolved{Name: name, Tree: expanded}, errs
}

// ResolveMount expands one configured mount document. Mounts do not merge catalog
// defaults or service catalog documents: each file under paths.mounts is the
// complete declaration for one fstab-backed mount unit.
func (c *Config) ResolveMount(name string) (Resolved, []string) {
	doc, ok := c.Mounts[name]
	if !ok {
		return Resolved{Name: name}, []string{fmt.Sprintf("unknown mount %q", name)}
	}
	body := stripMeta(doc.Body)
	vars, errs := c.expansionVariables(body, name)
	expanded, expErrs := expandTree(body, vars)
	errs = append(errs, expErrs...)
	return Resolved{Name: name, Tree: expanded}, errs
}

// MountNameByPath returns the configured mount name whose resolved path matches
// path. Empty means no configured mount currently owns that path.
func (c *Config) MountNameByPath(path string) string {
	cleanPath := cleanMountPath(path)
	for _, name := range c.MountNames {
		resolved, errs := c.ResolveMount(name)
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

// expandBinary desugars a top-level `binary` declaration into the legacy
// ${binary} variable plus a required binary preflight check for executable
// service/app binaries. Library catalog documents only use ${binary} as the
// watched file path, so they do not get an executable preflight.
func expandBinary(tree map[string]any, kind string) []string {
	raw, present := tree["binary"]
	if !present {
		return nil
	}
	candidates := cfgval.StringList(raw)
	if len(candidates) == 0 {
		delete(tree, "binary")
		return []string{"binary must be a non-empty path string or list"}
	}
	binary := topLevelBinaryForKind(tree, kind)
	delete(tree, "binary")
	if binary == "" {
		return []string{"binary must contain at least one non-empty path"}
	}

	vars, _ := tree["variables"].(map[string]any)
	if vars == nil {
		vars = map[string]any{}
	}
	vars["binary"] = binary
	tree["variables"] = vars

	if kind == kindLibrary {
		return nil
	}

	preflight, _ := tree["preflight"].(map[string]any)
	if preflight == nil {
		preflight = map[string]any{}
	}
	if _, exists := preflight["binary"]; !exists {
		preflight["binary"] = map[string]any{"type": "binary", "path": "${binary}"}
	}
	if len(preflight) > 0 {
		tree["preflight"] = preflight
	}
	return nil
}

// expandPidfile desugars a top-level `pidfile: <path>` or candidate list into
// two things that share the one declaration: (a) a `processes` pidfile selector,
// so the parent process and its descendants are discovered and monitored, and
// (b) a `pidfile` health check gated by `requires: [service]`, so a missing or
// stale pidfile is an error only while the service is active (which means the
// daemon died or lost its pidfile without the service manager noticing). The key
// is removed. An existing pidfile selector or a check already named `pidfile` is
// respected, not overwritten.
func expandPidfile(tree map[string]any) []string {
	raw, present := tree["pidfile"]
	if !present {
		return nil
	}
	delete(tree, "pidfile")
	paths := cfgval.StringList(raw)
	if len(paths) == 0 {
		return []string{"pidfile must be a non-empty path string or list"}
	}
	var errs []string
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			errs = append(errs, fmt.Sprintf("pidfile path %q must be absolute", path))
		}
	}
	pathValue := pidfilePathValue(paths)

	// (a) process-tree selector, unless the service already declares one.
	procs, _ := tree["processes"].(map[string]any)
	if procs == nil {
		procs = map[string]any{}
	}
	if !hasPidfileSelector(procs) {
		if _, exists := procs["pidfile"]; !exists {
			procs["pidfile"] = map[string]any{"type": "pidfile", "path": pathValue}
		}
	}
	if len(procs) > 0 {
		tree["processes"] = procs
	}

	// (b) gated health check, unless the service already defines one.
	checksMap, _ := tree["checks"].(map[string]any)
	if checksMap == nil {
		checksMap = map[string]any{}
	}
	if _, exists := checksMap["pidfile"]; !exists {
		checksMap["pidfile"] = map[string]any{
			"type":     "pidfile",
			"path":     pathValue,
			"requires": []any{"service"},
		}
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

// hasPidfileSelector reports whether a processes map already declares a pidfile
// selector (so the desugar does not add a second one).
func hasPidfileSelector(procs map[string]any) bool {
	for _, v := range procs {
		if m, ok := v.(map[string]any); ok {
			if cfgval.AsString(m["type"]) == "pidfile" {
				return true
			}
		}
	}
	return false
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
// of restart_on_change, for daemons whose config can be reloaded (udev rules,
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

// injectBuiltinVariables makes the document's identity available for ${...}
// expansion: ${name} (the resolved service name), ${display_name} (the
// display_name field, falling back to name), ${service} (the primary unit),
// ${host} (the detected hostname), ${hostname} (the short hostname, for
// host-keyed systemd instance units such as ceph-mon@${hostname}), ${init} (the
// detected init system), ${user} (the Sermo user, a fallback for service
// accounts), ${pidfile} (the conventional /run/<unit>.pid) and ${port} (the
// top-level `port:` field, when set). They let daemons parameterize strings — e.g. a tcp check
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
		appVars := collectVariablesForKind(stripMeta(doc.Body), doc.Kind)
		if exposeDefaults {
			for varName, value := range appVars {
				errs = append(errs, addAppVariable(out, source, varName, name, value)...)
			}
		}
		prefixes := []string{appVariablePrefix(name)}
		if doc.Name != name {
			prefixes = append(prefixes, appVariablePrefix(doc.Name))
		}
		for varName, value := range appVars {
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

// ResolveWatches returns the global `watches` section with ${var} expanded
// against the custom global variables and the host-level builtins. Watches have
// no per-watch builtins (name/port/pidfile). nil when no watches are configured.
func (c *Config) ResolveWatches() (map[string]any, []string) {
	raw, ok := c.Global.Raw["watches"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	vars := c.globalVars()
	injectHostBuiltins(vars)
	return expandTree(raw, vars)
}

// expandRestartOnChange desugars a `restart_on_change: {libraries: [...]}` block
// into one remediation rule per library that restarts the service when the
// library file changes. Each named library is resolved to its file via the
// matching library daemon, so the generated `changed:` condition carries a
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

// libraryPath resolves a library name to the file its library daemon watches
// (the `binary` variable). known is false when no library has that name; an
// empty path with known=true means the library declares no binary. Shared by
// expandRestartOnChange and the `changed: {library: X}` condition rewrite.
func (c *Config) libraryPath(lib string) (path string, known bool) {
	doc, ok := c.Libraries[lib]
	if !ok {
		return "", false
	}
	return daemonBinary(doc.Body), true
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
// only — a daemon/service that links an app of the same name is not a cycle.
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
			preflight[key] = check
		}
	}
	if len(preflight) > 0 {
		tree["preflight"] = preflight
	}
	return errs
}

// ResolveDaemon expands a daemon's own body — no service merge — so its
// concrete values (notably the binary path and preflight commands) can be
// inspected directly, as the `apps` command does. ${name} and ${display_name}
// are available; the returned errors mirror Resolve's.
func (c *Config) ResolveDaemon(name string) (Resolved, []string) {
	doc, ok := c.Daemons[name]
	if !ok {
		return Resolved{Name: name}, []string{fmt.Sprintf("unknown daemon %q", name)}
	}
	return c.resolveDoc(doc, name)
}

// catalogRegistry returns the registry that holds a given category's
// definitions (apps, libraries, else the daemon/service definitions).
func (c *Config) catalogRegistry(category string) map[string]*Document {
	switch category {
	case CategoryApp:
		return c.Apps
	case CategoryLibrary:
		return c.Libraries
	case CategoryPatterns:
		return c.Patterns
	default:
		return c.Daemons
	}
}

// ResolveCatalog expands a catalog definition from the registry for its category
// (service | app | library). It lets category-scoped listings (`apps`, `libs`,
// `services`) resolve a name in its own registry, since names may repeat across
// kinds.
func (c *Config) ResolveCatalog(category, name string) (Resolved, []string) {
	doc, ok := c.catalogRegistry(category)[name]
	if !ok {
		return Resolved{Name: name}, []string{fmt.Sprintf("unknown %s %q", category, name)}
	}
	return c.resolveDoc(doc, name)
}

// resolveDoc expands a single catalog document's own body (no service merge),
// shared by ResolveDaemon and the `apps` linkage (which resolves app documents).
func (c *Config) resolveDoc(doc *Document, name string) (Resolved, []string) {
	// Top level (daemon/service/catalog): its apps: links start a fresh app
	// chain. The top-level name is a different namespace than apps, so a daemon
	// linking an app of the same name is not a cycle.
	return c.resolveDocBody(doc, name, nil)
}

// resolveDocBody expands doc's own body and its apps: links, threading appChain
// (the app names already being resolved on this path) so expandAppsChain can
// detect a cyclic apps: linkage instead of recursing into a stack overflow.
func (c *Config) resolveDocBody(doc *Document, name string, appChain []string) (Resolved, []string) {
	body := stripMeta(doc.Body)
	vars, errs := c.expansionVariablesForKind(body, name, doc.Kind)
	errs = append(errs, expandBinary(body, doc.Kind)...)
	expanded, expErrs := expandTree(body, vars)
	errs = append(errs, expErrs...)
	errs = append(errs, c.expandAppsChain(expanded, appChain)...)
	errs = append(errs, c.expandAnalyze(expanded)...)
	errs = append(errs, expandPidfile(expanded)...)
	return Resolved{Name: name, Tree: expanded}, errs
}

// mergedService returns the merged-but-unexpanded body for a service, following
// its uses/clone layering. chain tracks the active clone path for cycle
// detection (section 8).
func (c *Config) mergedService(name string, chain []string) (map[string]any, error) {
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
		src, err := c.mergedService(clone, append(chain, name))
		if err != nil {
			return nil, err
		}
		merged = src
	} else {
		merged = c.defaultsPerService()
		if uses := cfgval.String(doc.Body["uses"]); uses != "" {
			daemon, ok := c.Daemons[uses]
			if !ok {
				return nil, fmt.Errorf("service %q uses unknown daemon %q", name, uses)
			}
			merged = mergeMaps(merged, stripMeta(daemon.Body))
		}
	}

	merged = mergeMaps(merged, stripMeta(doc.Body))
	applyDeletes(merged)
	return merged, nil
}

// defaultsPerService returns a fresh copy of just the per-service parts of the
// global defaults (section 8).
func (c *Config) defaultsPerService() map[string]any {
	out := map[string]any{}
	for _, key := range perServiceDefaults {
		if v, ok := c.Global.Defaults[key]; ok {
			out[key] = deepCopy(v)
		}
	}
	return out
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
