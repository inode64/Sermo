package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/process"
)

// Issue is a single validation finding, scoped to a document or "global".
type Issue struct {
	Scope string
	Msg   string
}

var validBackends = map[string]struct{}{"": {}, backendAuto: {}, backendSystemd: {}, backendOpenRC: {}}

// rejectedSecurityToggles are keys under `security:` that try to disable hard
// safety invariants and must never be honored.
var rejectedSecurityToggles = []string{
	"require_preflight_before_restart",
	"block_restart_on_active_lock",
	"allow_sigkill_by_default",
	"require_kill_selector",
}

var validGlobalPathKeys = set(
	pathKeyApps,
	pathKeyCatalog,
	pathKeyNetworks,
	pathKeyNotifiers,
	pathKeyRuntime,
	pathKeyServices,
	pathKeyState,
	pathKeyStorages,
	pathKeyTemplates,
	pathKeyWatches,
)

var validDefaultsKeys = set(
	"dry_run",
	"policy",
	"rule_window",
	"stop_policy",
	"variables",
)

// Validate returns all schema and safety issues for a loaded config. An empty
// slice means the current validators accept the configuration.
func Validate(cfg *Config) []Issue {
	var issues []Issue
	issues = append(issues, validateGlobal(cfg)...)
	issues = append(issues, cfg.validationIssues...)
	issues = append(issues, validateDocuments(cfg)...)
	issues = append(issues, validateServices(cfg)...)
	issues = append(issues, validateStorages(cfg)...)
	return issues
}

func validateGlobal(cfg *Config) []Issue {
	var issues []Issue
	raw := cfg.Global.Raw
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: "global", Msg: fmt.Sprintf(format, args...)})
	}

	validateEnableIfTree(raw, add)

	if engine, ok := raw["engine"].(map[string]any); ok {
		if backend := cfgval.String(engine["backend"]); !isValidBackend(backend) {
			add("engine.backend %q is not one of auto, systemd, openrc", backend)
		}
		for _, field := range []string{"interval", "default_timeout", "operation_timeout"} {
			if v, present := engine[field]; present && !isPositiveDuration(cfgval.String(v)) {
				add("engine.%s %q must be a valid positive duration", field, cfgval.String(v))
			}
		}
		if v, present := engine["startup_delay"]; present && !isNonNegativeDuration(cfgval.String(v)) {
			add("engine.startup_delay %q must be a valid non-negative duration (0 disables the wait)", cfgval.String(v))
		}
		if mode := cfgval.String(engine["user_lookup"]); !process.ValidUserLookupMode(mode) {
			add("engine.user_lookup %q is not one of auto, native, getent, numeric", mode)
		}
		if v, present := engine["user_lookup_timeout"]; present && !isPositiveDuration(cfgval.String(v)) {
			add("engine.user_lookup_timeout %q must be a valid positive duration", cfgval.String(v))
		}
		if v, present := engine["max_parallel_checks"]; present {
			if n, ok := cfgval.Int(v); !ok || n <= 0 {
				add("engine.max_parallel_checks must be an integer > 0")
			}
		}
		if v, present := engine["max_parallel_operations"]; present {
			if n, ok := cfgval.Int(v); !ok || n <= 0 {
				add("engine.max_parallel_operations must be an integer > 0")
			}
		}
		if v, present := engine["state_cache_size"]; present {
			if n, ok := cfgval.ByteSize(v); !ok || n == 0 {
				add("engine.state_cache_size must be a positive size with a K/M/G suffix (e.g. 64M)")
			}
		}
		for _, key := range []string{"access", "events", "diagnostics"} {
			if v, present := engine[key]; present {
				path := cfgval.AsString(v)
				if path == "" {
					add("engine.%s must be a non-empty absolute path when set", key)
				} else if !filepath.IsAbs(path) {
					add("engine.%s %q must be an absolute path", key, path)
				}
			}
		}
		if v, present := engine["diagnostics_interval"]; present {
			if cfgval.String(engine["diagnostics"]) == "" {
				add("engine.diagnostics_interval is set but engine.diagnostics is not configured")
			} else if !isPositiveDuration(cfgval.String(v)) {
				add("engine.diagnostics_interval %q must be a valid positive duration", cfgval.String(v))
			}
		}
	}

	if paths, ok := raw["paths"].(map[string]any); ok {
		for _, key := range slices.Sorted(maps.Keys(paths)) {
			if key == "locks" {
				add("paths.locks is not supported; runtime locks derive from paths.runtime")
				continue
			}
			if _, known := validGlobalPathKeys[key]; !known {
				add("paths.%s is not supported", key)
			}
		}
		if runtime := cfgval.String(paths[pathKeyRuntime]); runtime != "" && !filepath.IsAbs(runtime) {
			add("paths.runtime %q must be an absolute directory", runtime)
		}
		if stateDir := cfgval.String(paths[pathKeyState]); stateDir != "" && !filepath.IsAbs(stateDir) {
			add("paths.state %q must be an absolute directory", stateDir)
		}
		if templateDir := cfgval.String(paths[pathKeyTemplates]); templateDir != "" && !filepath.IsAbs(templateDir) {
			add("paths.templates %q must be an absolute directory", templateDir)
		}
		pathLists := map[string][]string{
			pathKeyApps:      cfg.Global.Apps,
			pathKeyCatalog:   cfg.Global.Catalog,
			pathKeyNetworks:  cfg.Global.Networks,
			pathKeyNotifiers: cfg.Global.Notifiers,
			pathKeyServices:  cfg.Global.Services,
			pathKeyStorages:  cfg.Global.Storages,
			pathKeyWatches:   cfg.Global.Watches,
		}
		for name, dirs := range pathLists {
			for _, dir := range dirs {
				if dir != "" && !filepath.IsAbs(dir) {
					add("paths.%s entry %q must be an absolute directory", name, dir)
				}
			}
		}
	}

	if security, ok := raw["security"].(map[string]any); ok {
		for _, key := range rejectedSecurityToggles {
			if _, present := security[key]; present {
				add("security.%s is a hard safety invariant and cannot be configured", key)
			}
		}
	}

	if webCfg, ok := raw["web"].(map[string]any); ok {
		validateWeb(webCfg, add)
	}

	notifiers := cfg.Notifiers()
	validateNotifiers(notifiers, cfg.Global.TemplateDir(), add)

	if _, present := raw["notify"]; present {
		validateNotifySelection("notify", raw["notify"], notifierNames(notifiers), add)
	}

	cooldown, present := defaultsCooldown(cfg.Global.Defaults)
	switch {
	case !present:
		add("defaults.policy.cooldown is required and must be a positive duration")
	case !isPositiveDuration(cooldown):
		add("defaults.policy.cooldown %q must be a valid positive duration", cooldown)
	}

	validateDefaultsKeys(cfg.Global.Defaults, add)
	validateDefaultsVariables(cfg.Global.Defaults, add)
	if v, present := cfg.Global.Defaults["dry_run"]; present {
		if _, ok := v.(bool); !ok {
			add("defaults.dry_run must be a boolean")
		}
	}
	// Nested-${} in a custom variable value, and any undefined ${var} used in a
	// watch, surface here (services get this via validateServices->Resolve).
	for _, e := range validateVariableValues(cfg.globalVars()) {
		add("defaults.variables: %s", e)
	}
	watches, watchErrs := cfg.ResolveWatches()
	if len(watchErrs) > 0 {
		for _, e := range watchErrs {
			add("watches: %s", e)
		}
	}
	if len(watches) > 0 {
		validateWatches(watches, filepath.Join(cfg.Global.RuntimeDir(), "locks"), notifierNames(notifiers), NotifyDefault(raw), add)
	}

	return issues
}

func validateDefaultsKeys(defaults map[string]any, add func(string, ...any)) {
	for _, key := range slices.Sorted(maps.Keys(defaults)) {
		if _, ok := validDefaultsKeys[key]; !ok {
			add("defaults.%s is not supported", key)
		}
	}
}

// registryLabel turns a document's registry namespace (registryKey) into the
// human term used in validation messages.
func registryLabel(key string) string {
	if key == catalogServiceKey {
		return "catalog service"
	}
	return key // "app", "lib", "patterns", "service", "storage"
}

func validateDocuments(cfg *Config) []Issue {
	var issues []Issue
	// Duplicate names are detected per registry namespace, so a catalog service
	// and an app may share a name (e.g. the `apache` catalog service and the
	// `apache` app that owns its binary), and a catalog service template and a
	// configured service may both be named `apache` without colliding.
	registryKeys := []string{
		catalogServiceKey, kindApp, kindLibrary, kindPatterns, kindService, kindStorage,
	}
	counts := map[string]map[string]int{}
	aliasOwners := map[string]map[string]string{}
	for _, key := range registryKeys {
		counts[key] = map[string]int{}
		aliasOwners[key] = map[string]string{}
	}

	for _, doc := range cfg.docs {
		scope := documentScope(doc)
		if d, present := doc.Body["description"]; present {
			if _, ok := d.(string); !ok {
				issues = append(issues, Issue{Scope: scope, Msg: "description must be a string"})
			}
		}
		if d, present := doc.Body["display_name"]; present {
			if _, ok := d.(string); !ok {
				issues = append(issues, Issue{Scope: scope, Msg: "display_name must be a string"})
			}
		}
		if d, present := doc.Body["category"]; present {
			if _, ok := d.(string); !ok {
				issues = append(issues, Issue{Scope: scope, Msg: "category must be a string"})
			}
		}
		addDoc := func(format string, args ...any) {
			issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf(format, args...)})
		}
		validateEnableIfTree(doc.Body, addDoc)
		validateFromFileVariables("variables", doc.Body["variables"], addDoc)
		issues = append(issues, validateBinaryVariables(doc, scope)...)
		issues = append(issues, validateVersionFrom(cfg, doc, scope)...)
		issues = append(issues, validateVersionsFrom(doc, scope)...)
		issues = append(issues, validateVersionsCurrentFrom(doc, scope)...)
		issues = append(issues, validateAppLinks(cfg, doc, scope)...)
		issues = append(issues, validateVersionMatch(doc, scope)...)
		switch doc.Kind {
		case kindApp, kindLibrary, kindPatterns, kindService, kindStorage:
		case "":
			issues = append(issues, Issue{Scope: scope, Msg: "document has no kind (expected app, lib, patterns, service or storage)"})
			continue
		default:
			issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("unknown kind %q (expected app, lib, patterns, service or storage)", doc.Kind)})
			continue
		}
		if doc.Name == "" {
			issues = append(issues, Issue{Scope: scope, Msg: "document has no name"})
			continue
		}
		if !validDocumentName(doc.Name) {
			issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("document name %q must be a simple name without path separators", doc.Name)})
		}
		counts[doc.registryKey()][doc.Name]++
	}

	for _, doc := range cfg.docs {
		kindCounts, knownKind := counts[doc.registryKey()]
		if !knownKind || doc.Name == "" {
			continue
		}
		scope := documentScope(doc)
		raw, present := doc.Body["aliases"]
		if !present {
			continue
		}
		aliases, err := cfgval.StrictStringArray(raw)
		if err != nil {
			issues = append(issues, Issue{Scope: scope, Msg: "aliases must be a list of simple names"})
			continue
		}
		seen := map[string]bool{}
		for _, alias := range aliases {
			switch {
			case alias == "":
				issues = append(issues, Issue{Scope: scope, Msg: "aliases must not contain empty names"})
				continue
			case !validDocumentName(alias):
				issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("alias %q must be a simple name without path separators", alias)})
				continue
			case alias == doc.Name:
				issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("alias %q duplicates the document name", alias)})
				continue
			case kindCounts[alias] > 0:
				issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("alias %q conflicts with a %s name", alias, registryLabel(doc.registryKey()))})
				continue
			case seen[alias]:
				issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("duplicate alias %q", alias)})
				continue
			}
			seen[alias] = true
			if owner := aliasOwners[doc.registryKey()][alias]; owner != "" && owner != doc.Name {
				issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("alias %q is already used by %s %q", alias, registryLabel(doc.registryKey()), owner)})
				continue
			}
			aliasOwners[doc.registryKey()][alias] = doc.Name
		}
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

func validateMaterializedNameCollisions(cfg *Config) []Issue {
	var issues []Issue
	for _, collision := range cfg.materializedNameCollisions {
		scope := collision.Kind + " " + collision.Name
		msg := fmt.Sprintf("materialized %s name %q from template %q conflicts with existing %s name", collision.Kind, collision.Name, collision.TemplateName, collision.Kind)
		if collision.ExistingPath != "" {
			msg += fmt.Sprintf(" at %s", collision.ExistingPath)
		}
		if collision.TemplatePath != "" {
			msg += fmt.Sprintf("; template path %s", collision.TemplatePath)
		}
		msg += "; remove one definition or adjust the template discovery"
		issues = append(issues, Issue{Scope: scope, Msg: msg})
	}
	return issues
}

func validateVersionMatch(doc *Document, scope string) []Issue {
	raw, present := doc.Body["version_match"]
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
	if doc.Kind == kindApp && checks.ReservedCommandEntry(doc.Body, "version") == nil {
		issues = append(issues, Issue{Scope: scope, Msg: "version_match requires a version command"})
	}
	return issues
}

func validateVersionFrom(cfg *Config, doc *Document, scope string) []Issue {
	raw, present := doc.Body["version_from"]
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
		source := cfgval.String(doc.Body["version_from"])
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
	versions, ok := doc.Body["versions"].(map[string]any)
	if !ok {
		return nil
	}
	raw, present := versions["current_from"]
	if !present {
		return nil
	}
	var issues []Issue
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf(format, args...)})
	}
	validateVersionsCurrentFromValue("versions.current_from", raw, add)
	return issues
}

func validateVersionsFrom(doc *Document, scope string) []Issue {
	versions, ok := doc.Body["versions"].(map[string]any)
	if !ok {
		return nil
	}
	raw, present := versions["from"]
	if !present {
		return nil
	}
	var issues []Issue
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf(format, args...)})
	}
	validateVersionsFromValue("versions.from", raw, add)
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
		add("%s must be a path string or list of path strings", path)
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
		add("%s must be a path string or list of path strings", path)
	}
}

func validateAppLinks(cfg *Config, doc *Document, scope string) []Issue {
	var issues []Issue
	raw, present := doc.Body["apps"]
	if !present {
		return issues
	}
	names, err := cfgval.StrictStringList(raw)
	if err != nil {
		return append(issues, Issue{Scope: scope, Msg: "apps must be a string or list of strings"})
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
	if vars, ok := doc.Body["variables"].(map[string]any); ok {
		raw := vars["binary"]
		if raw == nil {
			return issues
		}
		candidates, err := cfgval.StrictStringList(raw)
		if err != nil || len(candidates) == 0 {
			issues = append(issues, Issue{Scope: scope, Msg: "variables.binary must be a non-empty path string or list"})
			return issues
		}
		for _, path := range candidates {
			if !filepath.IsAbs(path) {
				issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("variables.binary path %q must be absolute", path)})
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

func validateStorages(cfg *Config) []Issue {
	var issues []Issue
	paths := map[string]string{}
	notifiers := notifierNames(cfg.Notifiers())
	defaultNotify := NotifyDefault(cfg.Global.Raw)
	for _, name := range cfg.StorageNames {
		if name == "" {
			continue
		}
		resolved, errs := cfg.ResolveStorage(name)
		for _, e := range errs {
			issues = append(issues, Issue{Scope: "storage " + name, Msg: e})
		}
		if resolved.Tree == nil {
			continue
		}
		issues = append(issues, validateStorage(name, resolved.Tree, notifiers, defaultNotify)...)
		path := filepath.Clean(cfgval.String(resolved.Tree["path"]))
		if path != "." && path != "" {
			if prev := paths[path]; prev != "" && prev != name {
				issues = append(issues, Issue{Scope: "storage " + name, Msg: fmt.Sprintf("path %q is already used by storage %q", path, prev)})
			} else {
				paths[path] = name
			}
		}
	}
	return issues
}

func validateStorage(name string, tree map[string]any, notifiers map[string]struct{}, defaultNotify []string) []Issue {
	var issues []Issue
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: "storage " + name, Msg: fmt.Sprintf(format, args...)})
	}

	allowed := set("name", "display_name", "description", "category", "path", "dry_run", "monitor", "interval", "capacity", "usage", "mount", "variables", "os")
	for _, key := range slices.Sorted(maps.Keys(tree)) {
		if _, ok := allowed[key]; !ok {
			add("key %q is not supported for kind: storage", key)
		}
	}

	path := cfgval.String(tree["path"])
	if path == "" {
		add("path is required")
	} else if !filepath.IsAbs(path) {
		add("path %q must be an absolute path", path)
	}
	if mode, present := tree["monitor"]; present {
		validateMonitorMode("monitor", mode, add)
	}
	if v, present := tree["dry_run"]; present {
		if _, ok := v.(bool); !ok {
			add("dry_run must be a boolean")
		}
	}
	if v, present := tree["interval"]; present && !isPositiveDuration(cfgval.String(v)) {
		add("interval %q must be a valid positive duration", cfgval.String(v))
	}
	if capacity, ok := tree["capacity"].(map[string]any); ok {
		validateStorageCapacity(name, path, tree, capacity, notifiers, defaultNotify, add)
	} else if _, present := tree["capacity"]; present {
		add("capacity must be a mapping")
	}
	if usage, ok := tree["usage"].(map[string]any); ok {
		validateStorageUsage(usage, notifiers, add)
	} else if _, present := tree["usage"]; present {
		add("usage must be a mapping")
	}
	if mount, ok := tree["mount"].(map[string]any); ok {
		validateStorageMount(mount, add)
	} else if _, present := tree["mount"]; present {
		add("mount must be a mapping")
	}

	for _, e := range validateVariableValues(collectVariables(tree)) {
		add("variables: %s", e)
	}
	return issues
}

func validateStorageCapacity(name, path string, tree, capacity map[string]any, notifiers map[string]struct{}, defaultNotify []string, add addFunc) {
	allowed := set("mounted", "for", "within", "then", "policy")
	for _, field := range checks.StoragePredFields {
		allowed[field] = struct{}{}
	}
	for _, key := range slices.Sorted(maps.Keys(capacity)) {
		if _, ok := allowed[key]; !ok {
			add("capacity key %q is not supported", key)
		}
	}
	check := map[string]any{"type": "storage", "path": path}
	for _, key := range append([]string{"mounted"}, checks.StoragePredFields...) {
		if v, present := capacity[key]; present {
			check[key] = v
		}
	}
	entry := map[string]any{"check": check}
	for _, key := range []string{"dry_run", "monitor", "interval"} {
		if v, present := tree[key]; present {
			entry[key] = v
		}
	}
	for _, key := range []string{"for", "within", "then", "policy"} {
		if v, present := capacity[key]; present {
			entry[key] = v
		}
	}
	validateWatches(map[string]any{name: entry}, "", notifiers, defaultNotify, func(format string, args ...any) {
		add(strings.Replace(fmt.Sprintf(format, args...), "watches."+name, "capacity", 1))
	})
}

func validateStorageUsage(usage map[string]any, notifiers map[string]struct{}, add addFunc) {
	allowed := set("processes", "users", "observed_for", "for", "within", "then")
	for _, key := range slices.Sorted(maps.Keys(usage)) {
		if _, ok := allowed[key]; !ok {
			add("usage key %q is not supported", key)
		}
	}
	for _, key := range []string{"processes", "users"} {
		raw, present := usage[key]
		if !present {
			continue
		}
		m, ok := raw.(map[string]any)
		if !ok {
			add("usage.%s must be a mapping {op, value}", key)
			continue
		}
		validateOpNumeric("usage."+key, m, add)
	}
	if v, present := usage["observed_for"]; present && !isPositiveDuration(cfgval.String(v)) {
		add("usage.observed_for %q must be a valid positive duration", cfgval.String(v))
	}
	validateWindow("usage", usage, add)
	if rawThen, present := usage["then"]; present {
		then, ok := rawThen.(map[string]any)
		if !ok {
			add("usage.then must be a mapping")
		} else if _, present := then["notify"]; present {
			validateNotifySelection("usage.then.notify", then["notify"], notifiers, add)
		}
	}
}

func validateStorageMount(mount map[string]any, add addFunc) {
	allowed := set("refcount", "umount", "stop_policy")
	for _, key := range slices.Sorted(maps.Keys(mount)) {
		if _, ok := allowed[key]; !ok {
			add("mount key %q is not supported", key)
		}
	}
	if v, present := mount["refcount"]; present {
		if _, ok := v.(bool); !ok {
			add("mount.refcount must be true or false")
		}
	}

	umount, _ := mount["umount"].(map[string]any)
	if _, present := mount["umount"]; present && umount == nil {
		add("mount.umount must be a mapping")
	}
	allowSIGKILL := false
	if umount != nil {
		allowedUmount := set("term_timeout", "kill_timeout", "allow_sigkill", "allow_lazy")
		for _, key := range slices.Sorted(maps.Keys(umount)) {
			if _, ok := allowedUmount[key]; !ok {
				add("mount.umount key %q is not one of term_timeout, kill_timeout, allow_sigkill, allow_lazy", key)
			}
		}
		for _, field := range []string{"term_timeout", "kill_timeout"} {
			if v, present := umount[field]; present && !isPositiveDuration(cfgval.String(v)) {
				add("mount.umount.%s %q must be a valid positive duration", field, cfgval.String(v))
			}
		}
		for _, field := range []string{"allow_sigkill", "allow_lazy"} {
			if v, present := umount[field]; present {
				b, ok := v.(bool)
				if !ok {
					add("mount.umount.%s must be true or false", field)
				}
				if field == "allow_sigkill" && ok && b {
					allowSIGKILL = true
				}
			}
		}
	}

	if sp, ok := mount["stop_policy"].(map[string]any); ok {
		force, _ := sp["force_kill"].(bool)
		if force {
			allowSIGKILL = true
		}
	} else if _, present := mount["stop_policy"]; present {
		add("mount.stop_policy must be a mapping")
	}
	validateStopPolicy(map[string]any{"stop_policy": mount["stop_policy"]}, func(format string, args ...any) {
		add("mount." + fmt.Sprintf(format, args...))
	})
	if allowSIGKILL {
		sp, _ := mount["stop_policy"].(map[string]any)
		_, hasKoi := sp["kill_only_if"].(map[string]any)
		if !hasKoi {
			add("mount.umount.allow_sigkill=true requires mount.stop_policy.kill_only_if")
		}
	}
}

// effectiveBackend returns the init backend validation should assume:
// SERMO_BACKEND, then explicit engine.backend, otherwise host-detected init.
func effectiveBackend(cfg *Config) string {
	if backend := strings.ToLower(envOverride("SERMO_BACKEND")); backend == backendSystemd || backend == backendOpenRC {
		return backend
	}
	if engine, ok := cfg.Global.Raw["engine"].(map[string]any); ok {
		if backend := cfgval.String(engine["backend"]); backend != "" && backend != backendAuto {
			return backend
		}
	}
	return detectedInit
}

func validateResolved(name string, tree map[string]any, runtime string, notifiers map[string]struct{}, services map[string]struct{}, backend string) []Issue {
	var issues []Issue
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: name, Msg: fmt.Sprintf(format, args...)})
	}

	if v, present := tree["interval"]; present && !isPositiveDuration(cfgval.String(v)) {
		add("interval %q must be a valid positive duration", cfgval.String(v))
	}

	if mode, present := tree["monitor"]; present {
		validateMonitorMode("monitor", mode, add)
	}
	if v, present := tree["dry_run"]; present {
		if _, ok := v.(bool); !ok {
			add("dry_run must be a boolean")
		}
	}
	if _, present := tree["remediation"]; present {
		add("remediation is not supported; use top-level dry_run")
	}

	cooldown, present := policyCooldown(tree)
	switch {
	case !present:
		add("policy.cooldown is required and must be positive after resolution")
	case !isPositiveDuration(cooldown):
		add("policy.cooldown %q must be a valid positive duration", cooldown)
	}

	walkScalars(tree, func(path, key, value string) {
		switch key {
		case "port":
			if n, err := strconv.Atoi(value); err != nil || n < 1 || n > 65535 {
				add("%s = %q must resolve to a port in 1..65535", path, value)
			}
		case "expect_status":
			if !validExpectStatus(value) {
				add("%s = %q must resolve to a valid HTTP status, class (2xx) or list", path, value)
			}
		}
	})

	locksDir := filepath.Join(runtime, "locks")
	validateCheckSection(tree, "checks", locksDir, add)
	validateCheckSection(tree, "preflight", locksDir, add)
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
