package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/emission"
	"sermo/internal/process"
)

// Issue is a single validation finding, scoped to a document or "global".
type Issue struct {
	Scope string
	Msg   string
}

var validBackends = map[string]struct{}{"": {}, backendAuto: {}, backendSystemd: {}, backendOpenRC: {}}

const (
	backendSummary        = backendAuto + ", " + backendSystemd + ", " + backendOpenRC
	initBackendSummary    = backendSystemd + ", " + backendOpenRC
	userLookupModeSummary = process.UserLookupAuto + ", " +
		process.UserLookupNative + ", " +
		process.UserLookupGetent + ", " +
		process.UserLookupNumeric
	mountUmountKeySummary = StopPolicyKeyTermTimeout + ", " +
		StopPolicyKeyKillTimeout

	securityKeyAllowSIGKILLByDefault         = "allow_sigkill_by_default"
	securityKeyBlockRestartOnActiveLock      = "block_restart_on_active_lock"
	securityKeyRequireKillSelector           = "require_kill_selector"
	securityKeyRequirePreflightBeforeRestart = "require_preflight_before_restart"

	validationAnalyzeMappingFormat   = "%s.analyze must be a mapping"
	validationBooleanFormat          = "%s must be a boolean"
	validationBooleanLiteralFormat   = "%s must be true or false"
	validationMappingFormat          = "%s must be a mapping"
	validationNonEmptyPathListFormat = "%s must be a non-empty path string or list"
	validationPathListFormat         = "%s must be a path string or list of path strings"
	validationPidfilesMappingMsg     = "pidfiles must be a mapping of process role to path string or candidate list"
	validationStringListFormat       = "%s must be a string or list of strings"
	validationTCPPortRangeFormat     = "%s must be an integer in %s"
)

// rejectedSecurityToggles are keys under `security:` that try to disable hard
// safety invariants and must never be honored.
var rejectedSecurityToggles = []string{
	securityKeyRequirePreflightBeforeRestart,
	securityKeyBlockRestartOnActiveLock,
	securityKeyAllowSIGKILLByDefault,
	securityKeyRequireKillSelector,
}

var validGlobalPathKeys = set(
	pathKeyApps,
	pathKeyNotifiers,
	pathKeyRuntime,
	pathKeyServices,
	pathKeyState,
	pathKeyTemplates,
	pathKeyWatches,
)

var validDefaultsKeys = set(
	keyDryRun,
	keyRestartOnChange,
	sectionPolicy,
	sectionRuleWindow,
	sectionStopPolicy,
	sectionVariables,
)

var validEngineKeys = set(
	keyInterval,
	EngineKeyAccess,
	EngineKeyArtifactInterval,
	EngineKeyBackend,
	EngineKeyDefaultTimeout,
	EngineKeyDiagnostics,
	EngineKeyDiagnosticsInterval,
	EngineKeyEvents,
	EngineKeyMaxParallelChecks,
	EngineKeyMaxParallelOperations,
	EngineKeyOperationTimeout,
	EngineKeyStartupDelay,
	EngineKeyStateCacheSize,
	EngineKeyUserLookup,
	EngineKeyUserLookupTimeout,
)

// Validate returns all schema and safety issues for a loaded config. An empty
// slice means the current validators accept the configuration.
func Validate(cfg *Config) []Issue {
	issues := make([]Issue, 0, len(cfg.validationIssues))
	issues = append(issues, validateGlobal(cfg)...)
	issues = append(issues, cfg.validationIssues...)
	issues = append(issues, validateDocuments(cfg)...)
	issues = append(issues, validateServices(cfg)...)
	return issues
}

func validateGlobal(cfg *Config) []Issue {
	var issues []Issue
	raw := cfg.Global.Raw
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: "global", Msg: fmt.Sprintf(format, args...)})
	}

	validateEnableIfTree(raw, add)
	validateGlobalEngine(raw, add)
	validateGlobalPaths(cfg, raw, add)
	validateGlobalSecurity(raw, add)
	validateGlobalWebAndEmission(raw, add)
	validateGlobalDefaults(cfg, raw, add)

	return issues
}

func validateGlobalEngine(raw map[string]any, add addFunc) {
	engine, ok := raw[SectionEngine].(map[string]any)
	if !ok {
		return
	}
	for _, key := range slices.Sorted(maps.Keys(engine)) {
		if _, allowed := validEngineKeys[key]; !allowed {
			add("%s is not supported", engineFieldPath(key))
		}
	}
	if backend := cfgval.String(engine[EngineKeyBackend]); !isValidBackend(backend) {
		add("%s %q is not one of %s", enginePathBackend, backend, backendSummary)
	}
	for _, field := range []string{keyInterval, EngineKeyArtifactInterval, EngineKeyDefaultTimeout, EngineKeyOperationTimeout} {
		if v, present := engine[field]; present && !isPositiveDuration(cfgval.String(v)) {
			add("%s %q must be a valid positive duration", engineFieldPath(field), cfgval.String(v))
		}
	}
	if v, present := engine[EngineKeyStartupDelay]; present && !isNonNegativeDuration(cfgval.String(v)) {
		add("%s %q must be a valid non-negative duration (0 disables the wait)", enginePathStartupDelay, cfgval.String(v))
	}
	if mode := cfgval.String(engine[EngineKeyUserLookup]); !process.ValidUserLookupMode(mode) {
		add("%s %q is not one of %s", enginePathUserLookup, mode, userLookupModeSummary)
	}
	if v, present := engine[EngineKeyUserLookupTimeout]; present && !isPositiveDuration(cfgval.String(v)) {
		add("%s %q must be a valid positive duration", enginePathUserLookupTimeout, cfgval.String(v))
	}
	validateGlobalEnginePositiveInt(engine, EngineKeyMaxParallelChecks, enginePathMaxParallelChecks, add)
	validateGlobalEnginePositiveInt(engine, EngineKeyMaxParallelOperations, enginePathMaxParallelOperations, add)
	if v, present := engine[EngineKeyStateCacheSize]; present {
		if n, ok := cfgval.ByteSize(v); !ok || n == 0 {
			add("%s must be a positive size with a K/M/G suffix (e.g. 64M)", enginePathStateCacheSize)
		}
	}
	for _, key := range []string{EngineKeyAccess, EngineKeyEvents, EngineKeyDiagnostics} {
		if v, present := engine[key]; present {
			field := engineFieldPath(key)
			path := cfgval.AsString(v)
			if path == "" {
				add("%s must be a non-empty absolute path when set", field)
			} else if !filepath.IsAbs(path) {
				add("%s %q must be an absolute path", field, path)
			}
		}
	}
	if v, present := engine[EngineKeyDiagnosticsInterval]; present {
		if cfgval.String(engine[EngineKeyDiagnostics]) == "" {
			add("%s is set but %s is not configured", enginePathDiagnosticsInterval, enginePathDiagnostics)
		} else if !isPositiveDuration(cfgval.String(v)) {
			add("%s %q must be a valid positive duration", enginePathDiagnosticsInterval, cfgval.String(v))
		}
	}
}

func validateGlobalEnginePositiveInt(engine map[string]any, key, path string, add addFunc) {
	if v, present := engine[key]; present {
		if n, ok := cfgval.Int(v); !ok || n <= 0 {
			add("%s must be an integer > 0", path)
		}
	}
}

func validateGlobalPaths(cfg *Config, raw map[string]any, add addFunc) {
	paths, ok := raw[sectionPaths].(map[string]any)
	if !ok {
		return
	}
	for _, key := range slices.Sorted(maps.Keys(paths)) {
		if key == pathKeyLocks {
			add("%s is not supported; runtime locks derive from %s", pathsPathLocks, pathsPathRuntime)
			continue
		}
		if _, known := validGlobalPathKeys[key]; !known {
			add("%s is not supported", pathsFieldPath(key))
		}
	}
	for _, path := range []struct{ key, value string }{
		{pathKeyRuntime, cfgval.String(paths[pathKeyRuntime])},
		{pathKeyState, cfgval.String(paths[pathKeyState])},
		{pathKeyTemplates, cfgval.String(paths[pathKeyTemplates])},
	} {
		if path.value != "" && !filepath.IsAbs(path.value) {
			add("%s %q must be an absolute directory", pathsFieldPath(path.key), path.value)
		}
	}
	for name, dirs := range map[string][]string{
		pathKeyApps: cfg.Global.Apps, pathKeyNotifiers: cfg.Global.Notifiers,
		pathKeyServices: cfg.Global.Services, pathKeyWatches: cfg.Global.Watches,
	} {
		for _, dir := range dirs {
			if dir != "" && !filepath.IsAbs(dir) {
				add("%s entry %q must be an absolute directory", pathsFieldPath(name), dir)
			}
		}
	}
}

func validateGlobalSecurity(raw map[string]any, add addFunc) {
	security, ok := raw[sectionSecurity].(map[string]any)
	if !ok {
		return
	}
	for _, key := range rejectedSecurityToggles {
		if _, present := security[key]; present {
			add("%s is a hard safety invariant and cannot be configured", securityFieldPath(key))
		}
	}
}

func validateGlobalWebAndEmission(raw map[string]any, add addFunc) {
	if webCfg, ok := raw[SectionWeb].(map[string]any); ok {
		validateWeb(webCfg, add)
	}
	validateEmission(raw, emission.Section, add)
}

func validateGlobalDefaults(cfg *Config, raw map[string]any, add addFunc) {
	notifiers := cfg.Notifiers()
	validateNotifiers(notifiers, cfg.Global.TemplateDir(), add)
	if _, present := raw[sectionNotify]; present {
		validateNotifySelection(sectionNotify, raw[sectionNotify], notifierNames(notifiers), add)
	}
	cooldown, present := defaultsCooldown(cfg.Global.Defaults)
	switch {
	case !present:
		add("%s is required and must be a positive duration", defaultsPathPolicyCooldown)
	case !isPositiveDuration(cooldown):
		add("%s %q must be a valid positive duration", defaultsPathPolicyCooldown, cooldown)
	}
	validateDefaultsKeys(cfg.Global.Defaults, add)
	validateDefaultsVariables(cfg.Global.Defaults, add)
	validateDefaultsRestartOnChange(cfg.Global.Defaults, add)
	if v, present := cfg.Global.Defaults[keyDryRun]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, defaultsFieldPath(keyDryRun))
		}
	}
	for _, e := range validateVariableValues(cfg.globalVars()) {
		add("%s: %s", defaultsPathVariables, e)
	}
	watches, watchErrs := cfg.ResolveWatches()
	for _, e := range watchErrs {
		add("watches: %s", e)
	}
	if len(watches) > 0 {
		validateWatches(watches, filepath.Join(cfg.Global.RuntimeDir(), pathKeyLocks), notifierNames(notifiers), NotifyDefault(raw), add)
	}
}

func validateDefaultsKeys(defaults map[string]any, add func(string, ...any)) {
	for _, key := range slices.Sorted(maps.Keys(defaults)) {
		if _, ok := validDefaultsKeys[key]; !ok {
			add("%s is not supported", defaultsFieldPath(key))
		}
	}
}

func validateDefaultsRestartOnChange(defaults map[string]any, add addFunc) {
	raw, present := defaults[keyRestartOnChange]
	if !present {
		return
	}
	roc, ok := raw.(map[string]any)
	if !ok {
		add("%s must be a mapping", defaultsFieldPath(keyRestartOnChange))
		return
	}
	allowed := set(keyRestartConfig, keyRestartVersion)
	for _, key := range slices.Sorted(maps.Keys(roc)) {
		if _, ok := allowed[key]; !ok {
			add("%s is not supported", defaultsRestartOnChangeFieldPath(key))
		}
	}
	validateRestartOnChangeFlags(defaultsFieldPath(keyRestartOnChange), roc, add)
}

// registryLabel turns a document's registry namespace (registryKey) into the
// human term used in validation messages.
func registryLabel(key string) string {
	if key == catalogServiceKey {
		return "catalog service"
	}
	return key // "app", "lib", "patterns", "service"
}

func validateDocuments(cfg *Config) []Issue {
	var issues []Issue
	// Duplicate names are detected per registry namespace, so a catalog service
	// and an app may share a name (e.g. the `apache` catalog service and the
	// `apache` app that owns its binary), and a catalog service template and a
	// configured service may both be named `apache` without colliding.
	registryKeys := []string{
		catalogServiceKey, kindApp, kindLibrary, kindPatterns, kindService,
	}
	counts := map[string]map[string]int{}
	aliasOwners := map[string]map[string]string{}
	for _, key := range registryKeys {
		counts[key] = map[string]int{}
		aliasOwners[key] = map[string]string{}
	}

	for _, doc := range cfg.docs {
		docIssues, countName := validateDocument(cfg, doc)
		issues = append(issues, docIssues...)
		if countName {
			counts[doc.registryKey()][doc.Name]++
		}
	}

	for _, doc := range cfg.docs {
		issues = append(issues, validateDocumentAliases(doc, counts, aliasOwners)...)
	}

	for _, key := range registryKeys {
		label := registryLabel(key)
		for _, name := range slices.Sorted(maps.Keys(counts[key])) {
			if counts[key][name] > 1 {
				issues = append(issues, Issue{Scope: label + " " + name, Msg: "duplicate " + label + " name"})
			}
		}
	}
	issues = append(issues, validateMaterializedNameCollisions(cfg)...)
	return issues
}

func validateDocument(cfg *Config, doc *Document) ([]Issue, bool) {
	scope := documentScope(doc)
	issues := validateDocumentMetadata(doc, scope)
	addDoc := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf(format, args...)})
	}
	validateEnableIfTree(doc.Body, addDoc)
	validateFromFileVariables(doc.Body[sectionVariables], addDoc)
	issues = append(issues, validateBinaryVariables(doc, scope)...)
	issues = append(issues, validateVersionFrom(cfg, doc, scope)...)
	issues = append(issues, validateVersionsFrom(doc, scope)...)
	issues = append(issues, validateVersionsCurrentFrom(doc, scope)...)
	issues = append(issues, validateAppLinks(cfg, doc, scope)...)
	issues = append(issues, validateVersionMatch(doc, scope)...)
	issues = append(issues, validateDocumentInterval(doc, scope)...)
	if !validDocumentKind(doc.Kind) {
		return append(issues, invalidDocumentKindIssue(doc, scope)), false
	}
	if doc.Name == "" {
		return append(issues, Issue{Scope: scope, Msg: "document has no name"}), false
	}
	if !validDocumentName(doc.Name) {
		issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("document name %q must be a simple name without path separators", doc.Name)})
	}
	return issues, true
}

func validateDocumentMetadata(doc *Document, scope string) []Issue {
	fields := []struct{ key, label string }{
		{keyDescription, "description"},
		{keyDisplayName, "display_name"},
		{keyCategory, "category"},
	}
	var issues []Issue
	for _, field := range fields {
		if value, present := doc.Body[field.key]; present {
			if _, ok := value.(string); !ok {
				issues = append(issues, Issue{Scope: scope, Msg: field.label + " must be a string"})
			}
		}
	}
	return issues
}

func validateDocumentInterval(doc *Document, scope string) []Issue {
	if doc.Kind != kindApp && doc.Kind != kindLibrary {
		return nil
	}
	if value, present := doc.Body[keyInterval]; present && !isPositiveDuration(cfgval.String(value)) {
		return []Issue{{Scope: scope, Msg: fmt.Sprintf("interval %q must be a valid positive duration", cfgval.String(value))}}
	}
	return nil
}

func validDocumentKind(kind string) bool {
	switch kind {
	case kindApp, kindLibrary, kindPatterns, kindService:
		return true
	default:
		return false
	}
}

func invalidDocumentKindIssue(doc *Document, scope string) Issue {
	if doc.Kind == "" {
		return Issue{Scope: scope, Msg: "document has no kind (expected " + kindSummary + ")"}
	}
	return Issue{Scope: scope, Msg: fmt.Sprintf("unknown kind %q (expected %s)", doc.Kind, kindSummary)}
}

func validateDocumentAliases(doc *Document, counts map[string]map[string]int, aliasOwners map[string]map[string]string) []Issue {
	kindCounts, knownKind := counts[doc.registryKey()]
	if !knownKind || doc.Name == "" {
		return nil
	}
	scope := documentScope(doc)
	raw, present := doc.Body[keyAliases]
	if !present {
		return nil
	}
	aliases, err := cfgval.StrictStringArray(raw)
	if err != nil {
		return []Issue{{Scope: scope, Msg: "aliases must be a list of simple names"}}
	}
	var issues []Issue
	seen := map[string]bool{}
	for _, alias := range aliases {
		if issue := validateDocumentAlias(alias, doc, kindCounts, aliasOwners[doc.registryKey()], seen, scope); issue != nil {
			issues = append(issues, *issue)
			continue
		}
		seen[alias] = true
		aliasOwners[doc.registryKey()][alias] = doc.Name
	}
	return issues
}

func validateDocumentAlias(alias string, doc *Document, kindCounts map[string]int, aliasOwners map[string]string, seen map[string]bool, scope string) *Issue {
	var message string
	switch {
	case alias == "":
		message = "aliases must not contain empty names"
	case !validDocumentName(alias):
		message = fmt.Sprintf("alias %q must be a simple name without path separators", alias)
	case alias == doc.Name:
		message = fmt.Sprintf("alias %q duplicates the document name", alias)
	case kindCounts[alias] > 0:
		message = fmt.Sprintf("alias %q conflicts with a %s name", alias, registryLabel(doc.registryKey()))
	case seen[alias]:
		message = fmt.Sprintf("duplicate alias %q", alias)
	case aliasOwners[alias] != "" && aliasOwners[alias] != doc.Name:
		message = fmt.Sprintf("alias %q is already used by %s %q", alias, registryLabel(doc.registryKey()), aliasOwners[alias])
	}
	if message == "" {
		return nil
	}
	return &Issue{Scope: scope, Msg: message}
}

func validateMaterializedNameCollisions(cfg *Config) []Issue {
	issues := make([]Issue, 0, len(cfg.materializedNameCollisions))
	for _, collision := range cfg.materializedNameCollisions {
		scope := collision.Kind + " " + collision.Name
		msg := fmt.Sprintf("materialized %s name %q from template %q conflicts with existing %s name", collision.Kind, collision.Name, collision.TemplateName, collision.Kind)
		if collision.ExistingPath != "" {
			msg += " at " + collision.ExistingPath
		}
		if collision.TemplatePath != "" {
			msg += "; template path " + collision.TemplatePath
		}
		msg += "; remove one definition or adjust the template discovery"
		issues = append(issues, Issue{Scope: scope, Msg: msg})
	}
	return issues
}

func validateVersionMatch(doc *Document, scope string) []Issue {
	raw, present := doc.Body[checks.CheckKeyVersionMatch]
	if !present {
		return nil
	}
	var issues []Issue
	if doc.Kind != kindApp {
		issues = append(issues, Issue{Scope: scope, Msg: "version_match is only supported on app catalog documents"})
	}
	if _, warn := checks.ParseVersionMatcher(raw); warn != "" {
		issues = append(issues, Issue{Scope: scope, Msg: "version_match " + warn})
	}
	if doc.Kind == kindApp && checks.ReservedCommandEntry(doc.Body, ServiceMonitorKeyVersion) == nil {
		issues = append(issues, Issue{Scope: scope, Msg: "version_match requires a version command"})
	}
	return issues
}

func validateVersionFrom(cfg *Config, doc *Document, scope string) []Issue {
	raw, present := doc.Body[keyVersionFrom]
	if !present {
		return nil
	}
	var issues []Issue
	if doc.Kind != kindApp {
		issues = append(issues, Issue{Scope: scope, Msg: "version_from is only supported on app catalog documents"})
	}
	source, ok := raw.(string)
	if !ok || source == "" {
		return append(issues, Issue{Scope: scope, Msg: "version_from must be a non-empty app name"})
	}
	if !validDocumentName(source) {
		return append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("version_from %q must be a simple name without path separators", source)})
	}
	if doc.Kind != kindApp {
		return issues
	}
	provider, ok := cfg.Apps[source]
	if !ok {
		return append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("version_from references unknown app %q", source)})
	}
	if provider.Name == doc.Name {
		return append(issues, Issue{Scope: scope, Msg: "version_from must not reference itself"})
	}
	if cycle := versionFromCycle(cfg, doc.Name); len(cycle) > 0 {
		issues = append(issues, Issue{Scope: scope, Msg: "version_from cycle detected: " + strings.Join(cycle, " -> ")})
	}
	return issues
}

func versionFromCycle(cfg *Config, start string) []string {
	seen := map[string]int{}
	var chain []string
	for name := start; ; {
		if idx, ok := seen[name]; ok {
			return append(chain[idx:], name)
		}
		seen[name] = len(chain)
		chain = append(chain, name)
		doc := cfg.Apps[name]
		if doc == nil {
			return nil
		}
		source := cfgval.String(doc.Body[keyVersionFrom])
		if source == "" {
			return nil
		}
		provider := cfg.Apps[source]
		if provider == nil {
			return nil
		}
		name = provider.Name
	}
}

func validateVersionsCurrentFrom(doc *Document, scope string) []Issue {
	versions, ok := doc.Body[keyVersions].(map[string]any)
	if !ok {
		return nil
	}
	raw, present := versions[keyVersionsCurrentFrom]
	if !present {
		return nil
	}
	var issues []Issue
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf(format, args...)})
	}
	validateVersionsCurrentFromValue(versionsPathCurrentFrom, raw, add)
	return issues
}

func validateVersionsFrom(doc *Document, scope string) []Issue {
	versions, ok := doc.Body[keyVersions].(map[string]any)
	if !ok {
		return nil
	}
	raw, present := versions[keyVersionsFrom]
	if !present {
		return nil
	}
	var issues []Issue
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf(format, args...)})
	}
	validateVersionsFromValue(versionsPathFrom, raw, add)
	return issues
}

func validateVersionsFromValue(path string, raw any, add addFunc) {
	switch v := raw.(type) {
	case string:
		if v == "" {
			add("%s must be a non-empty path string", path)
		}
	case []any:
		for i, item := range v {
			validateVersionsFromValue(fmt.Sprintf("%s[%d]", path, i), item, add)
		}
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(v)) {
			if key != backendSystemd && key != backendOpenRC {
				add("%s.%s is not supported; use systemd or openrc", path, key)
				continue
			}
			validateVersionsFromBranch(fmt.Sprintf("%s.%s", path, key), v[key], add)
		}
	default:
		add("%s must be a path string, list of path strings, or map with systemd/openrc", path)
	}
}

func validateVersionsFromBranch(path string, raw any, add addFunc) {
	switch raw.(type) {
	case string, []any:
		validateVersionsFromValue(path, raw, add)
	default:
		add(validationPathListFormat, path)
	}
}

func validateVersionsCurrentFromValue(path string, raw any, add addFunc) {
	switch v := raw.(type) {
	case string:
		if v == "" {
			add("%s must be a non-empty path string", path)
		}
	case []any:
		for i, item := range v {
			validateVersionsCurrentFromValue(fmt.Sprintf("%s[%d]", path, i), item, add)
		}
	default:
		add(validationPathListFormat, path)
	}
}

func validateAppLinks(cfg *Config, doc *Document, scope string) []Issue {
	var issues []Issue
	raw, present := doc.Body[keyApps]
	if !present {
		return issues
	}
	names, err := cfgval.StrictStringList(raw)
	if err != nil {
		return append(issues, Issue{Scope: scope, Msg: fmt.Sprintf(validationStringListFormat, keyApps)})
	}
	for _, name := range names {
		if name == "" || strings.Contains(name, "${") {
			continue
		}
		if !validDocumentName(name) {
			issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("apps references invalid app name %q", name)})
			continue
		}
		if _, ok := cfg.Apps[name]; !ok {
			issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("apps references unknown app %q", name)})
		}
	}
	return issues
}

func validateBinaryVariables(doc *Document, scope string) []Issue {
	var issues []Issue
	if vars, ok := doc.Body[sectionVariables].(map[string]any); ok {
		raw := vars[VariableKeyBinary]
		if raw == nil {
			return issues
		}
		candidates, err := cfgval.StrictStringList(raw)
		if err != nil || len(candidates) == 0 {
			issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf(validationNonEmptyPathListFormat, variablePath(VariableKeyBinary))})
			return issues
		}
		for _, path := range candidates {
			if !filepath.IsAbs(path) {
				issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("%s path %q must be absolute", variablePath(VariableKeyBinary), path)})
			}
		}
	} else {
		return issues
	}
	return issues
}

func validDocumentName(name string) bool {
	return name != "." && name != ".." && !strings.Contains(name, "/") && !strings.Contains(name, `\`)
}

func validateServices(cfg *Config) []Issue {
	var issues []Issue
	defined := notifierNames(cfg.Notifiers())
	services := map[string]struct{}{}
	for _, n := range cfg.ServiceNames {
		services[n] = struct{}{}
	}
	seen := map[string]struct{}{}
	addIssue := func(issue Issue) {
		key := issue.Scope + "\x00" + issue.Msg
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		issues = append(issues, issue)
	}
	for _, name := range cfg.ServiceNames {
		if name == "" {
			continue
		}
		for _, pruneOptional := range []bool{false, true} {
			resolved, errs := cfg.resolveService(name, pruneOptional)
			for _, e := range errs {
				addIssue(Issue{Scope: name, Msg: e})
			}
			if resolved.Tree == nil {
				continue
			}
			for _, issue := range validateResolved(name, resolved.Tree, cfg.Global.RuntimeDir(), defined, services, effectiveBackend(cfg)) {
				addIssue(issue)
			}
		}
	}
	return issues
}

func validateStorageMount(mount map[string]any, add addFunc) {
	allowed := set(MountKeyRefcount, MountKeyUmount, MountKeyStopPolicy)
	for _, key := range slices.Sorted(maps.Keys(mount)) {
		if _, ok := allowed[key]; !ok {
			add("mount key %q is not supported", key)
		}
	}
	if v, present := mount[MountKeyRefcount]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanLiteralFormat, mountPathRefcount)
		}
	}

	umount, _ := mount[MountKeyUmount].(map[string]any)
	if _, present := mount[MountKeyUmount]; present && umount == nil {
		add("%s must be a mapping", mountPathUmount)
	}
	if umount != nil {
		allowedUmount := set(StopPolicyKeyTermTimeout, StopPolicyKeyKillTimeout)
		for _, key := range slices.Sorted(maps.Keys(umount)) {
			if _, ok := allowedUmount[key]; !ok {
				add("%s key %q is not one of %s", mountPathUmount, key, mountUmountKeySummary)
			}
		}
		for _, field := range []string{StopPolicyKeyTermTimeout, StopPolicyKeyKillTimeout} {
			if v, present := umount[field]; present && !isPositiveDuration(cfgval.String(v)) {
				add("%s %q must be a valid positive duration", mountUmountFieldPath(field), cfgval.String(v))
			}
		}
	}

	if sp, ok := mount[sectionStopPolicy].(map[string]any); ok {
		allowedStopPolicy := set(keyKillOnlyIf)
		for _, key := range slices.Sorted(maps.Keys(sp)) {
			if _, ok := allowedStopPolicy[key]; !ok {
				add("%s key %q is not one of %s", mountPathStopPolicy, key, keyKillOnlyIf)
			}
		}
	} else if _, present := mount[sectionStopPolicy]; present {
		add("%s must be a mapping", mountPathStopPolicy)
	}
	validateStopPolicy(map[string]any{sectionStopPolicy: mount[sectionStopPolicy]}, func(format string, args ...any) {
		add(mountPath + "." + fmt.Sprintf(format, args...))
	})
}

// effectiveBackend returns the init backend validation should assume:
// SERMO_BACKEND, then explicit engine.backend, otherwise host-detected init.
func effectiveBackend(cfg *Config) string {
	if backend := strings.ToLower(envOverride(EnvBackendOverride)); backend == backendSystemd || backend == backendOpenRC {
		return backend
	}
	if engine, ok := cfg.Global.Raw[SectionEngine].(map[string]any); ok {
		if backend := cfgval.String(engine[EngineKeyBackend]); backend != "" && backend != backendAuto {
			return backend
		}
	}
	return detectedInit
}

const keyUnsupportedRemediation = "remediation"

func validateResolved(name string, tree map[string]any, runtime string, notifiers map[string]struct{}, services map[string]struct{}, backend string) []Issue {
	var issues []Issue
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: name, Msg: fmt.Sprintf(format, args...)})
	}

	if v, present := tree[keyInterval]; present && !isPositiveDuration(cfgval.String(v)) {
		add("interval %q must be a valid positive duration", cfgval.String(v))
	}

	if mode, present := tree[keyMonitor]; present {
		validateMonitorMode(keyMonitor, mode, add)
	}
	if v, present := tree[keyDryRun]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, keyDryRun)
		}
	}
	if _, present := tree[keyUnsupportedRemediation]; present {
		add("remediation is not supported; use top-level dry_run")
	}
	if _, present := tree[emission.Section]; present {
		add("%s is not supported on services; configure global emission or a rules.*.emission override", emission.Section)
	}

	cooldown, present := policyCooldown(tree)
	switch {
	case !present:
		add("%s is required and must be positive after resolution", policyPathCooldown)
	case !isPositiveDuration(cooldown):
		add("%s %q must be a valid positive duration", policyPathCooldown, cooldown)
	}

	walkScalars(tree, func(path, key, value string) {
		switch key {
		case checks.CheckKeyPort:
			if n, ok := cfgval.Int(value); !ok || !validTCPPort(n) {
				add("%s = %q must resolve to a port in %s", path, value, cfgval.TCPPortRange())
			}
		case checks.CheckKeyExpectStatus:
			if !validExpectStatus(value) {
				add("%s = %q must resolve to a valid HTTP status, class (2xx) or list", path, value)
			}
		}
	})

	locksDir := filepath.Join(runtime, pathKeyLocks)
	validateCheckSection(tree, sectionChecks, locksDir, add)
	validateCheckSection(tree, sectionPreflight, locksDir, add)
	validateProcesses(tree, add)
	validatePidfiles(tree, add)
	validateStopPolicy(tree, add)
	validatePolicyExtras(tree, add)
	validateControl(tree, add)
	validateServiceField(tree, add)
	validateAlsoService(tree, add)
	validateCascade(name, tree, services, add)
	validateCommands(tree, add)
	validateReload(tree, backend, add)
	validateRuleWindow(tree, add)
	validateServiceMonitors(tree, notifiers, add)
	validateServiceWatches(tree, locksDir, notifiers, NotifyDefault(tree), add)
	validateRules(tree, notifiers, add)

	return issues
}

type addFunc func(format string, args ...any)

func documentScope(doc *Document) string {
	kind := doc.Kind
	if kind == "" {
		kind = "document"
	}
	if doc.Name != "" {
		return kind + " " + doc.Name
	}
	return fmt.Sprintf("%s %s", kind, filepath.Base(doc.Path))
}
