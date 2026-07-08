package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/rules"
)

const (
	unknownCatalogFormat        = "unknown %s %q"
	unknownCatalogServiceFormat = "unknown catalog service %q"
	unknownServiceFormat        = "unknown service %q"
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
		return Resolved{Name: name}, []string{fmt.Sprintf(unknownServiceFormat, name)}
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

// ResolveStorage returns one resolved storage watch in the tree shape mount
// operations consume: top-level metadata/path/mount copied from a normal
// `check.type: storage` watch. It is an adapter for mount-facing code; storage
// watches are configured through paths.watches like every other host watch.
func (c *Config) ResolveStorage(name string) (Resolved, []string) {
	watches, errs := c.ResolveWatches()
	entry, ok := watches[name].(map[string]any)
	if !ok {
		return Resolved{Name: name}, append(errs, fmt.Sprintf("unknown storage watch %q", name))
	}
	tree, storageErrs := storageTreeFromWatch(name, entry)
	errs = append(errs, storageErrs...)
	return Resolved{Name: name, Tree: tree}, errs
}

// StorageNameByPath returns the configured storage watch name whose resolved path
// matches path. Empty means no configured storage watch currently owns that path.
func (c *Config) StorageNameByPath(path string) string {
	cleanPath := cleanMountPath(path)
	for _, name := range c.StorageWatchNames() {
		resolved, errs := c.ResolveStorage(name)
		if len(errs) > 0 {
			continue
		}
		if cleanMountPath(cfgval.String(resolved.Tree[keyPath])) == cleanPath {
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

// StorageMountNames returns the storage watches that expose mount operations.
func (c *Config) StorageMountNames() []string {
	if c == nil {
		return nil
	}
	names := c.StorageWatchNames()
	out := make([]string, 0, len(names))
	for _, name := range names {
		resolved, errs := c.ResolveStorage(name)
		if len(errs) > 0 {
			continue
		}
		if _, ok := resolved.Tree[keyMount].(map[string]any); ok {
			out = append(out, name)
		}
	}
	return out
}

// StorageWatchNames returns configured host watches backed by a storage check.
func (c *Config) StorageWatchNames() []string {
	if c == nil {
		return nil
	}
	watches, _ := c.ResolveWatches()
	out := make([]string, 0, len(watches))
	for _, name := range slices.Sorted(maps.Keys(watches)) {
		entry, _ := watches[name].(map[string]any)
		check, _ := entry[WatchKeyCheck].(map[string]any)
		if cfgval.String(check[checks.CheckKeyType]) == checks.CheckTypeStorage {
			out = append(out, name)
		}
	}
	return out
}

func storageTreeFromWatch(name string, entry map[string]any) (map[string]any, []string) {
	check, _ := entry[WatchKeyCheck].(map[string]any)
	if cfgval.String(check[checks.CheckKeyType]) != checks.CheckTypeStorage {
		return nil, []string{fmt.Sprintf("watch %q is not a storage watch", name)}
	}
	path := cfgval.String(check[checks.CheckKeyPath])
	tree := map[string]any{keyPath: path}
	for _, key := range []string{keyDisplayName, keyDescription, keyCategory, keyDryRun, keyMonitor, keyInterval, keyMount} {
		if v, present := entry[key]; present {
			tree[key] = deepCopy(v)
		}
	}
	return tree, nil
}

// Service-artifact kinds. Each top-level artifact declaration desugars into an
// auto-generated health check whose name and type are the kind string; the kind
// is also the tree key the declaration is read from (and, for socket/lockfile,
// removed at). A watches.<kind> entry collides with the generated check.
const (
	artifactPidfile  = checks.CheckTypePidfile
	artifactSocket   = checks.CheckTypeSocket
	artifactLockfile = checks.CheckTypeLockfile
)

const (
	keyReloadOnChange  = "reload_on_change"
	keyRestartOnChange = "restart_on_change"
	keyLibraries       = checks.CheckTypeLibraries
	keyAnalyze         = checks.CheckKeyAnalyze
	keyAnalyzeSilence  = "silence"
	keyAnalyzeUse      = "use"
	keyRuleID          = checks.CheckKeyID
)

// expandPidfile validates a top-level `pidfile: <path>` or candidate list and
// adds a gated `pidfile` health check. The top-level declaration remains in the
// resolved tree as the service's single pidfile source; process discovery and
// OpenRC signal reload derive their internal pidfile selector from it.
func expandPidfile(tree map[string]any) []string {
	raw, present := tree[ServiceKeyPidfile]
	if !present {
		return nil
	}
	decl, errs := parseServiceArtifactPaths(artifactPidfile, raw)
	if len(decl.paths) == 0 {
		return errs
	}
	pathValue := serviceArtifactPathValue(decl.paths)
	tree[ServiceKeyPidfile] = pathValue

	// Gated health check, unless the service already defines one.
	ensureServiceArtifactCheck(tree, artifactPidfile, artifactPidfile, pathValue, decl.optional)
	return errs
}

// expandPidfiles validates `pidfiles: {role: path-or-candidates}` and adds one
// gated pidfile health check per role. Unlike `pidfile: [...]`, whose list is a
// set of alternative paths for one process, `pidfiles` declares several process
// roles that must each have a live pidfile while the service is active.
func expandPidfiles(tree map[string]any) []string {
	raw, present := tree[ServiceKeyPidfiles]
	if !present {
		return nil
	}
	var errs []string
	if _, hasPidfile := tree[ServiceKeyPidfile]; hasPidfile {
		errs = append(errs, "pidfile and pidfiles are mutually exclusive")
	}
	pidfiles, ok := raw.(map[string]any)
	if !ok {
		return append(errs, validationPidfilesMappingMsg)
	}

	normalized := make(map[string]any, len(pidfiles))
	checksMap, _ := tree[sectionChecks].(map[string]any)
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
			errs = append(errs, fmt.Sprintf(validationNonEmptyPathListFormat, "pidfiles."+role))
			continue
		}
		for _, path := range paths {
			if !filepath.IsAbs(path) {
				errs = append(errs, fmt.Sprintf("pidfiles.%s path %q must be absolute", role, path))
			}
		}
		pathValue := serviceArtifactPathValue(paths)
		normalized[role] = pathValue
		checkName := artifactPidfile + "-" + role
		if _, exists := checksMap[checkName]; !exists {
			checksMap[checkName] = map[string]any{
				keyType:     artifactPidfile,
				keyPath:     pathValue,
				keyRequires: []any{ServiceKeyService},
			}
		}
	}
	tree[ServiceKeyPidfiles] = normalized
	tree[sectionChecks] = checksMap
	return errs
}

// expandSocket desugars a top-level `socket:` declaration into a gated health
// check. A service-created runtime socket should not block start/restart
// preflight: it is checked while the service is active, like pidfiles.
func expandSocket(tree map[string]any) []string {
	raw, present := tree[artifactSocket]
	if !present {
		return nil
	}
	delete(tree, artifactSocket)

	decl, errs := parseServiceArtifactPaths(artifactSocket, raw)
	if len(decl.paths) > 0 {
		ensureServiceArtifactCheck(tree, artifactSocket, artifactSocket, serviceArtifactPathValue(decl.paths), decl.optional)
	}
	return errs
}

// expandLockfile desugars a top-level `lockfile:` declaration into a gated
// health check. It is for service-owned runtime lock artifacts, not Sermo
// operation locks.
func expandLockfile(tree map[string]any) []string {
	raw, present := tree[artifactLockfile]
	if !present {
		return nil
	}
	delete(tree, artifactLockfile)

	decl, errs := parseServiceArtifactPaths(artifactLockfile, raw)
	if len(decl.paths) > 0 {
		ensureServiceArtifactCheck(tree, artifactLockfile, artifactLockfile, serviceArtifactPathValue(decl.paths), decl.optional)
	}
	return errs
}

type serviceArtifactPaths struct {
	paths    []string
	optional bool
}

func parseServiceArtifactPaths(kind string, raw any) (serviceArtifactPaths, []string) {
	pathRaw := raw
	optional := false
	if m, ok := raw.(map[string]any); ok {
		pathRaw = m[keyPath]
		optional = cfgval.Bool(m[keyOptional])
	}
	paths := cfgval.StringList(pathRaw)
	if len(paths) == 0 {
		return serviceArtifactPaths{}, []string{fmt.Sprintf("%s must be a non-empty path string, list or {path: ...} mapping", kind)}
	}
	var errs []string
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			errs = append(errs, fmt.Sprintf("%s path %q must be absolute", kind, path))
		}
	}
	return serviceArtifactPaths{paths: paths, optional: optional}, errs
}

func ensureServiceArtifactCheck(tree map[string]any, name, checkType string, pathValue any, optional bool) {
	checksMap, _ := tree[sectionChecks].(map[string]any)
	if checksMap == nil {
		checksMap = map[string]any{}
	}
	if _, exists := checksMap[name]; !exists {
		entry := map[string]any{
			keyType:     checkType,
			keyPath:     pathValue,
			keyRequires: []any{ServiceKeyService},
		}
		if optional {
			entry[keyOptional] = true
		}
		checksMap[name] = entry
	}
	tree[sectionChecks] = checksMap
}

func serviceArtifactPathValue(paths []string) any {
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
// Check-only service watches are processed before they desugar into `checks:`.
func (c *Config) expandAnalyze(tree map[string]any) []string {
	var errs []string
	if checks, ok := tree[sectionChecks].(map[string]any); ok {
		errs = append(errs, c.expandAnalyzeSection(sectionChecks, checks)...)
	}

	watches, ok := tree[sectionWatches].(map[string]any)
	if ok {
		for _, name := range slices.Sorted(maps.Keys(watches)) {
			entry, ok := watches[name].(map[string]any)
			if !ok {
				continue
			}
			check, ok := entry[WatchKeyCheck].(map[string]any)
			if !ok {
				continue
			}
			errs = append(errs, c.expandAnalyzeEntry("watches."+name+".check", check)...)
		}
	}

	return errs
}

func (c *Config) expandAnalyzeSection(section string, entries map[string]any) []string {
	var errs []string
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		entry, ok := entries[name].(map[string]any)
		if !ok {
			continue
		}
		errs = append(errs, c.expandAnalyzeEntry(section+"."+name, entry)...)
	}
	return errs
}

func (c *Config) expandAnalyzeEntry(scope string, entry map[string]any) []string {
	analyze, ok := entry[keyAnalyze].(map[string]any)
	if !ok {
		if _, present := entry[keyAnalyze]; present {
			return []string{fmt.Sprintf(validationAnalyzeMappingFormat, scope)}
		}
		return nil
	}
	ruleList, errs := c.resolveAnalyze(scope+".analyze", analyze)
	entry[keyAnalyze] = map[string]any{rules.SectionRules: ruleList}
	return errs
}

// resolveAnalyze builds the flat, ordered rule list for one check's analyze block.
func (c *Config) resolveAnalyze(scope string, analyze map[string]any) ([]any, []string) {
	var errs []string

	silence := map[string]bool{}
	for _, id := range cfgval.StringList(analyze[keyAnalyzeSilence]) {
		silence[id] = true
	}
	seenSilence := map[string]bool{}

	var ruleList []any
	ids := map[string]bool{}
	addRule := func(r any) {
		rm, ok := r.(map[string]any)
		if !ok {
			errs = append(errs, scope+": each rule must be a mapping")
			return
		}
		id := cfgval.AsString(rm[keyRuleID])
		if id != "" && ids[id] {
			errs = append(errs, fmt.Sprintf("%s: duplicate rule id %q", scope, id))
			return
		}
		ids[id] = true
		ruleList = append(ruleList, r)
	}

	// Local rules come FIRST so the service takes precedence: a local rule (e.g.
	// an `ok` whitelist for a known-benign line) wins over an inherited rule that
	// would otherwise match the same line, since evaluation is first-match-wins.
	if local, ok := analyze[rules.SectionRules].([]any); ok {
		for _, r := range local {
			addRule(r)
		}
	}

	// Inherited rules from each `use` set, in order, minus silenced ids.
	for _, setName := range cfgval.StringList(analyze[keyAnalyzeUse]) {
		doc, ok := c.Patterns[setName]
		if !ok {
			errs = append(errs, fmt.Sprintf("%s.use references %q, which is not a patterns set", scope, setName))
			continue
		}
		setRules, _ := doc.Body[rules.SectionRules].([]any)
		for _, r := range setRules {
			if rm, ok := r.(map[string]any); ok {
				if id := cfgval.AsString(rm[keyRuleID]); id != "" && silence[id] {
					seenSilence[id] = true
					continue
				}
			}
			addRule(r)
		}
	}

	// A silence id that matched no inherited rule is a typo worth catching.
	for _, id := range cfgval.StringList(analyze[keyAnalyzeSilence]) {
		if !seenSilence[id] {
			errs = append(errs, fmt.Sprintf("%s.silence references id %q not present in the inherited sets", scope, id))
		}
	}

	return ruleList, errs
}

// expandReloadOnChange desugars a `reload_on_change: {paths: [...]}` block into
// one remediation rule per path that *reloads* the service (re-reads its config
// in place, no restart) when that file changes. It is the non-disruptive analog
// of restart_on_change, for catalog services whose config can be reloaded (udev rules,
// nginx vhosts, named zones, …). The block is removed; an empty paths list is a
// no-op.
func expandReloadOnChange(tree map[string]any) []string {
	roc, ok := tree[keyReloadOnChange].(map[string]any)
	if !ok {
		if _, present := tree[keyReloadOnChange]; present {
			delete(tree, keyReloadOnChange)
			return []string{"reload_on_change must be a mapping with a paths list"}
		}
		return nil
	}
	delete(tree, keyReloadOnChange)

	ruleMap, _ := tree[rules.SectionRules].(map[string]any)
	if ruleMap == nil {
		ruleMap = map[string]any{}
	}
	var errs []string
	for i, p := range cfgval.StringList(roc[keyPaths]) {
		if p == "" {
			errs = append(errs, "reload_on_change.paths entry is empty")
			continue
		}
		key := fmt.Sprintf("reload-on-change-%d", i+1)
		if _, exists := ruleMap[key]; exists {
			errs = append(errs, fmt.Sprintf("reload_on_change would overwrite existing rule %q; rename that rule", key))
			continue
		}
		ruleMap[key] = map[string]any{
			rules.RuleFieldType: string(rules.RuleRemediation),
			rules.RuleFieldIf:   map[string]any{rules.ConditionChanged: map[string]any{rules.FieldPath: p}},
			rules.RuleFieldThen: map[string]any{rules.RuleFieldAction: string(rules.ActionReload)},
		}
	}
	if len(ruleMap) > 0 {
		tree[rules.SectionRules] = ruleMap
	}
	return errs
}

// expandServiceWatches desugars service watches that are not runtime side effects:
// check-only entries become a generated `checks:` probe, and entries whose `then`
// declares a rule-class action (restart/start/stop/reload/resume → remediation,
// block → guard, alert → alert) become that check plus the equivalent `rules:`
// entry. What remains under `watches:` is only the fire-and-forget entries
// (hook/notify/expand/kill), built by the Watch runtime.
//
// The generated check embeds the watch's `check:` block verbatim and carries
// verify/requires/optional/interval when present. The rule's condition polarity
// follows the check type: a health check fires on failure (`failed`), a condition
// check on its threshold (`active`), matching checks.IsHealthType. The check and
// rule take the watch's name; a collision with an existing check/rule is an
// error. Reusing an existing check is expressed with an explicit rules: entry.
func expandServiceWatches(tree map[string]any) []string {
	watches, ok := tree[sectionWatches].(map[string]any)
	if !ok {
		return nil
	}
	checksMap, _ := tree[sectionChecks].(map[string]any)
	if checksMap == nil {
		checksMap = map[string]any{}
	}
	rulesMap, _ := tree[rules.SectionRules].(map[string]any)
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
		rawThen, hasThen := entry[rules.RuleFieldThen]
		then, _ := rawThen.(map[string]any)
		action := cfgval.String(then[rules.RuleFieldAction])
		if !hasThen {
			check, ok := entry[WatchKeyCheck].(map[string]any)
			if !ok {
				add("watches.%s.check is required", name)
				continue
			}
			if _, ok := promoteServiceWatchCheck(checksMap, name, entry, check, add); !ok {
				continue
			}
			delete(watches, name)
			continue
		}
		if action == "" || !isRuleClassAction(action) {
			continue // fire-and-forget watch (or invalid action): left for validateServiceWatches
		}
		// Validate the action grammar here (this entry is removed before the
		// resolved-tree validators run, so they never see it).
		validateWatchThenAction("watches."+name, action, then, add)
		check, ok := entry[WatchKeyCheck].(map[string]any)
		if !ok {
			add("watches.%s.check is required", name)
			continue
		}
		if _, exists := rulesMap[name]; exists {
			add("watches.%s would overwrite existing rule %q; rename the watch", name, name)
			continue
		}

		target, ok := promoteServiceWatchCheck(checksMap, name, entry, check, add)
		if !ok {
			continue
		}
		rulesMap[name] = buildServiceWatchRule(entry, then, action, target)

		delete(watches, name)
	}

	if len(checksMap) > 0 {
		tree[sectionChecks] = checksMap
	}
	if len(rulesMap) > 0 {
		tree[rules.SectionRules] = rulesMap
	}
	if len(watches) == 0 {
		delete(tree, sectionWatches)
	}
	return errs
}

type serviceWatchRuleTarget struct {
	checkName string
	checkType string
}

var serviceWatchCheckEntryFields = [...]string{keyEnabled, keyVerify, keyRequires, keyOptional, keyInterval}

// promoteServiceWatchCheck promotes an embedded watch check to checks.<watch-name>,
// returning the generated rule target.
func promoteServiceWatchCheck(checksMap map[string]any, name string, entry, check map[string]any, add func(string, ...any)) (serviceWatchRuleTarget, bool) {
	if _, exists := checksMap[name]; exists {
		add("watches.%s would overwrite existing check %q; rename the watch", name, name)
		return serviceWatchRuleTarget{}, false
	}
	checkType := cfgval.String(check[checks.CheckKeyType])
	if checkType == "" {
		add("watches.%s.check.type is required", name)
		return serviceWatchRuleTarget{}, false
	}
	genCheck := cloneMap(check)
	for _, k := range serviceWatchCheckEntryFields {
		if v, has := entry[k]; has {
			genCheck[k] = v
		}
	}
	checksMap[name] = genCheck
	return serviceWatchRuleTarget{checkName: name, checkType: checkType}, true
}

func buildServiceWatchRule(entry, then map[string]any, action string, target serviceWatchRuleTarget) map[string]any {
	rule := map[string]any{rules.RuleFieldIf: serviceWatchRuleCondition(target)}
	if w, has := entry[rules.RuleFieldFor]; has {
		rule[rules.RuleFieldFor] = w
	}
	if w, has := entry[rules.RuleFieldWithin]; has {
		rule[rules.RuleFieldWithin] = w
	}

	thenOut := map[string]any{rules.RuleFieldAction: action}
	if msg := cfgval.String(then[rules.RuleFieldMessage]); msg != "" {
		thenOut[rules.RuleFieldMessage] = msg
	}
	switch rules.ActionType(action) {
	case rules.ActionBlock:
		rule[rules.RuleFieldType] = string(rules.RuleGuard)
		if b := cfgval.StringList(then[rules.RuleFieldBlocks]); len(b) > 0 {
			rule[rules.RuleFieldBlocks] = then[rules.RuleFieldBlocks]
		}
	case rules.ActionAlert:
		rule[rules.RuleFieldType] = string(rules.RuleAlert)
	default:
		rule[rules.RuleFieldType] = string(rules.RuleRemediation)
	}
	// A rule's notify is an entry-level field (ParseRules reads entry[rules.RuleFieldNotify]),
	// not part of then; a guard never notifies.
	if action != string(rules.ActionBlock) {
		if n, has := then[rules.RuleFieldNotify]; has {
			rule[rules.RuleFieldNotify] = n
		}
	}
	rule[rules.RuleFieldThen] = thenOut
	return rule
}

// serviceWatchRuleCondition preserves watch polarity: health checks fire on
// failure, while condition checks fire when their threshold is active.
func serviceWatchRuleCondition(target serviceWatchRuleTarget) map[string]any {
	operand := map[string]any{rules.FieldCheck: target.checkName}
	if checks.IsHealthType(target.checkType) {
		return map[string]any{rules.ConditionFailed: operand}
	}
	return map[string]any{rules.ConditionActive: operand}
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
const (
	builtinPidfileDir = "/run"
	pidfileExt        = ".pid"
)

func injectBuiltinVariables(vars map[string]string, name string, merged map[string]any) {
	if _, ok := vars[keyName]; !ok {
		vars[keyName] = name
	}
	if _, ok := vars[keyDisplayName]; !ok {
		vars[keyDisplayName] = DisplayName(merged, name)
	}
	if _, ok := vars[VariableKeyService]; !ok {
		vars[VariableKeyService] = ServiceUnit(merged, name)
	}
	injectHostBuiltins(vars)
	// ${pidfile} falls back to the conventional /run/<unit>.pid; an explicit
	// `pidfile` variable always wins.
	if _, ok := vars[VariableKeyPidfile]; !ok {
		vars[VariableKeyPidfile] = builtinPidfileDir + "/" + vars[VariableKeyService] + pidfileExt
	}
	// ${port} mirrors the top-level `port:` field; unlike the others it has no
	// fallback, so it is injected only when the field is set — leaving ${port}
	// undefined (and so a clear error) when nothing provides a port.
	if _, ok := vars[VariableKeyPort]; !ok {
		if p := cfgval.String(merged[VariableKeyPort]); p != "" {
			vars[VariableKeyPort] = p
		}
	}
}

// injectHostBuiltins fills the service-independent (host-level) builtins —
// host/hostname/init/user — when absent. Shared by injectBuiltinVariables and
// the watch expansion (watches have no service-specific builtins).
func injectHostBuiltins(vars map[string]string) {
	if _, ok := vars[VariableKeyHost]; !ok {
		vars[VariableKeyHost] = detectedHost
	}
	if _, ok := vars[VariableKeyHostname]; !ok {
		vars[VariableKeyHostname] = detectedHostname
	}
	if _, ok := vars[VariableKeyInit]; !ok {
		vars[VariableKeyInit] = detectedInit
	}
	if _, ok := vars[VariableKeyUser]; !ok {
		vars[VariableKeyUser] = detectedUser
	}
}

// globalVars returns the custom variables declared under `defaults.variables`,
// processed through collectVariables so they get the same env (${env:...}) and
// list-first-existing handling as per-service variables. They form the lowest
// explicit layer (a service's own variables override them; builtins fill gaps).
func (c *Config) globalVars() map[string]string {
	return collectVariables(map[string]any{sectionVariables: c.Global.Defaults[sectionVariables]})
}

func (c *Config) expansionVariables(tree map[string]any, name string) (map[string]string, []string) {
	return c.expansionVariablesForKind(tree, name, cfgval.String(tree[keyKind]))
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
	names := cfgval.StringList(tree[keyApps])
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

// ResolveWatches returns the global `watches` section with ${var} expanded
// against the custom global variables and the host-level builtins. Watches have
// no per-watch builtins (name/port/pidfile).
// nil when no watches are configured.
func (c *Config) ResolveWatches() (map[string]any, []string) {
	raw := map[string]any{}
	if configured, ok := c.Global.Raw[sectionWatches].(map[string]any); ok {
		for name, entry := range configured {
			raw[name] = deepCopy(entry)
		}
	}
	if len(raw) == 0 {
		return nil, nil
	}
	c.applyWatchDefaults(raw)
	vars := c.globalVars()
	injectHostBuiltins(vars)
	expanded, expErrs := expandTree(raw, vars)
	return expanded, expErrs
}

// expandRestartOnChange desugars a `restart_on_change: {libraries: [...]}` block
// into one remediation rule per library that restarts the service when the
// library file changes. Each named library is resolved to its file via the
// matching library, so the generated `changed:` condition carries a
// concrete path. The block is removed; unknown or non-library references error.
func (c *Config) expandRestartOnChange(tree map[string]any) []string {
	roc, ok := tree[keyRestartOnChange].(map[string]any)
	if !ok {
		return nil
	}
	delete(tree, keyRestartOnChange)

	var errs []string
	libraries, _ := tree[rules.SectionRules].(map[string]any)
	if libraries == nil {
		libraries = map[string]any{}
	}
	for _, lib := range cfgval.StringList(roc[keyLibraries]) {
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
			rules.RuleFieldType: string(rules.RuleRemediation),
			rules.RuleFieldIf: map[string]any{
				rules.ConditionChanged: map[string]any{rules.FieldLibrary: lib, rules.FieldPath: path},
			},
			rules.RuleFieldThen: map[string]any{rules.RuleFieldAction: string(rules.ActionRestart)},
		}
	}
	if len(libraries) > 0 {
		tree[rules.SectionRules] = libraries
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
	rulesMap, ok := tree[rules.SectionRules].(map[string]any)
	if !ok {
		return nil
	}
	var errs []string
	for _, name := range slices.Sorted(maps.Keys(rulesMap)) {
		rule, ok := rulesMap[name].(map[string]any)
		if !ok {
			continue
		}
		if ifNode, ok := rule[rules.RuleFieldIf].(map[string]any); ok {
			errs = append(errs, c.fillChangedLibraryPaths(ifNode, rules.SectionRules+"."+name)...)
		}
	}
	return errs
}

// fillChangedLibraryPaths walks one condition node (recursing through and/or/
// not) and rewrites its changed-library leaf, collecting resolution errors.
func (c *Config) fillChangedLibraryPaths(node map[string]any, scope string) []string {
	var errs []string
	for _, key := range []string{rules.ConditionAnd, rules.ConditionOr} {
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
	if child, ok := node[rules.ConditionNot].(map[string]any); ok {
		errs = append(errs, c.fillChangedLibraryPaths(child, scope)...)
	}
	ch, ok := node[rules.ConditionChanged].(map[string]any)
	if !ok {
		return errs
	}
	lib := cfgval.String(ch[rules.FieldLibrary])
	if lib == "" || cfgval.String(ch[rules.FieldPath]) != "" {
		return errs
	}
	path, known := c.libraryPath(lib)
	switch {
	case !known:
		errs = append(errs, fmt.Sprintf("%s: changed references %q, which is not a library", scope, lib))
	case path == "":
		errs = append(errs, fmt.Sprintf("%s: library %q has no binary to watch", scope, lib))
	default:
		ch[rules.FieldPath] = path
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
	_, present := tree[keyApps]
	names := cfgval.StringList(tree[keyApps])
	delete(tree, keyApps)
	if !present {
		return nil
	}

	var errs []string
	preflight, _ := tree[sectionPreflight].(map[string]any)
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
		appPre, _ := resolved.Tree[sectionPreflight].(map[string]any)
		for checkName, check := range appPre {
			key := fmt.Sprintf("%s-%s", name, checkName)
			if _, exists := preflight[key]; exists {
				errs = append(errs, fmt.Sprintf("apps preflight key %q would overwrite an existing preflight check; rename one of the checks", key))
				continue
			}
			if checkName == checks.DataKeyVersion {
				if match, present := resolved.Tree[checks.CheckKeyVersionMatch]; present {
					if checkMap, ok := check.(map[string]any); ok {
						check = maps.Clone(checkMap)
						check.(map[string]any)[checks.CheckKeyVersionMatch] = match
					}
				}
			}
			preflight[key] = check
		}
	}
	if len(preflight) > 0 {
		tree[sectionPreflight] = preflight
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
		return Resolved{Name: name}, []string{fmt.Sprintf(unknownCatalogServiceFormat, name)}
	}
	doc, ok := c.CatalogServices[canonicalName]
	if !ok {
		return Resolved{Name: name}, []string{fmt.Sprintf(unknownCatalogServiceFormat, name)}
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
		return Resolved{Name: name}, []string{fmt.Sprintf(unknownCatalogFormat, category, name)}
	}
	doc := c.catalogRegistry(category)[canonicalName]
	if doc == nil {
		return Resolved{Name: name}, []string{fmt.Sprintf(unknownCatalogFormat, category, name)}
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
		return nil, fmt.Errorf(unknownServiceFormat, name)
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
		return nil, fmt.Errorf(unknownServiceFormat, name)
	}

	var merged map[string]any
	if clone := cfgval.String(doc.Body[keyClone]); clone != "" {
		// clone and uses are mutually exclusive: the clone branch ignores uses
		// entirely, so accepting both would silently drop the catalog service the author
		// asked to inherit. Surface it instead.
		if uses := cfgval.String(doc.Body[ServiceKeyUses]); uses != "" {
			return nil, fmt.Errorf("service %q sets both clone and uses, which are mutually exclusive", name)
		}
		src, err := c.mergedService(clone, append(chain, name))
		if err != nil {
			return nil, err
		}
		merged = src
	} else {
		merged = c.defaultsPerService()
		if uses := cfgval.String(doc.Body[ServiceKeyUses]); uses != "" {
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

func (c *Config) applyWatchDefaults(raw map[string]any) {
	v, ok := c.Global.Defaults[keyDryRun]
	if !ok {
		return
	}
	for _, entry := range raw {
		watch, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if _, present := watch[keyDryRun]; !present {
			watch[keyDryRun] = deepCopy(v)
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
