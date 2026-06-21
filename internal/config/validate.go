package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/process"
)

// Issue is a single validation finding, scoped to a document or "global".
type Issue struct {
	Scope string
	Msg   string
}

var validBackends = map[string]struct{}{"": {}, "auto": {}, "systemd": {}, "openrc": {}}

// rejectedSecurityToggles are keys under `security:` that try to disable hard
// safety invariants and must never be honored.
var rejectedSecurityToggles = []string{
	"require_preflight_before_restart",
	"block_restart_on_active_lock",
	"allow_sigkill_by_default",
	"require_kill_selector",
}

// Validate returns all schema and safety issues for a loaded config. An empty
// slice means the current validators accept the configuration.
func Validate(cfg *Config) []Issue {
	var issues []Issue
	issues = append(issues, validateGlobal(cfg)...)
	issues = append(issues, validateDocuments(cfg)...)
	issues = append(issues, validateServices(cfg)...)
	issues = append(issues, validateMounts(cfg)...)
	return issues
}

func validateGlobal(cfg *Config) []Issue {
	var issues []Issue
	raw := cfg.Global.Raw
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: "global", Msg: fmt.Sprintf(format, args...)})
	}

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
	}

	if paths, ok := raw["paths"].(map[string]any); ok {
		if _, present := paths["locks"]; present {
			add("paths.locks is not supported; runtime locks derive from paths.runtime")
		}
		for _, key := range []string{"includes", "enabled", "profiles"} {
			if _, present := paths[key]; present {
				add("paths.%s is not supported; use explicit paths.catalog/services/apps/notifiers/storages/networks/watches/mounts", key)
			}
		}
		if runtime := cfgval.String(paths["runtime"]); runtime != "" && !filepath.IsAbs(runtime) {
			add("paths.runtime %q must be an absolute directory", runtime)
		}
		if stateDir := cfgval.String(paths["state"]); stateDir != "" && !filepath.IsAbs(stateDir) {
			add("paths.state %q must be an absolute directory", stateDir)
		}
		if templateDir := cfgval.String(paths["templates"]); templateDir != "" && !filepath.IsAbs(templateDir) {
			add("paths.templates %q must be an absolute directory", templateDir)
		}
		pathLists := map[string][]string{
			"apps":      cfg.Global.Apps,
			"catalog":   cfg.Global.Catalog,
			"mounts":    cfg.Global.Mounts,
			"networks":  cfg.Global.Networks,
			"notifiers": cfg.Global.Notifiers,
			"services":  cfg.Global.Services,
			"storages":  cfg.Global.Storages,
			"watches":   cfg.Global.Watches,
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

	notifiers, _ := raw["notifiers"].(map[string]any)
	validateNotifiers(notifiers, cfg.Global.TemplateDir(), add)

	if _, present := raw["notify"]; present {
		validateNotifySelection("notify", cfgval.StringList(raw["notify"]), notifierNames(notifiers), add)
	}

	if watches, ok := raw["watches"].(map[string]any); ok {
		validateWatches(watches, filepath.Join(cfg.Global.RuntimeDir(), "locks"), notifierNames(notifiers), NotifyDefault(raw), add)
	}

	cooldown, present := defaultsCooldown(cfg.Global.Defaults)
	switch {
	case !present:
		add("defaults.policy.cooldown is required and must be a positive duration")
	case !isPositiveDuration(cooldown):
		add("defaults.policy.cooldown %q must be a valid positive duration", cooldown)
	}

	validateDefaultsVariables(cfg.Global.Defaults, add)
	// Nested-${} in a custom variable value, and any undefined ${var} used in a
	// watch, surface here (services get this via validateServices->Resolve).
	for _, e := range validateVariableValues(cfg.globalVars()) {
		add("defaults.variables: %s", e)
	}
	if _, errs := cfg.ResolveWatches(); len(errs) > 0 {
		for _, e := range errs {
			add("watches: %s", e)
		}
	}

	return issues
}

func validateDocuments(cfg *Config) []Issue {
	var issues []Issue
	// Duplicate names are detected per kind, so a daemon and an app may share a
	// name (e.g. the `apache` service and the `apache` app that owns its binary).
	counts := map[string]map[string]int{
		kindDaemon: {}, kindApp: {}, kindLibrary: {}, kindPatterns: {}, kindService: {}, kindMount: {},
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
		issues = append(issues, validateBinaryVariables(doc, scope)...)
		issues = append(issues, validateVersionFrom(cfg, doc, scope)...)
		switch doc.Kind {
		case kindDaemon, kindApp, kindLibrary, kindPatterns, kindService, kindMount:
		case "":
			issues = append(issues, Issue{Scope: scope, Msg: "document has no kind (expected daemon, app, lib, patterns, service or mount)"})
			continue
		default:
			issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("unknown kind %q (expected daemon, app, lib, patterns, service or mount)", doc.Kind)})
			continue
		}
		if doc.Name == "" {
			issues = append(issues, Issue{Scope: scope, Msg: "document has no name"})
			continue
		}
		if !validDocumentName(doc.Name) {
			issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("document name %q must be a simple name without path separators", doc.Name)})
		}
		counts[doc.Kind][doc.Name]++
	}

	for _, kind := range []string{kindDaemon, kindApp, kindLibrary, kindPatterns, kindService, kindMount} {
		for _, name := range slices.Sorted(maps.Keys(counts[kind])) {
			if counts[kind][name] > 1 {
				issues = append(issues, Issue{Scope: kind + " " + name, Msg: "duplicate " + kind + " name"})
			}
		}
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

func validateBinaryVariables(doc *Document, scope string) []Issue {
	var issues []Issue
	if vars, ok := doc.Body["variables"].(map[string]any); ok {
		raw := vars["binary"]
		if raw == nil {
			return issues
		}
		candidates := cfgval.StringList(raw)
		if len(candidates) == 0 {
			issues = append(issues, Issue{Scope: scope, Msg: "variables.binary must be a non-empty path string or list"})
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
	for _, name := range cfg.ServiceNames {
		if name == "" {
			continue
		}
		resolved, errs := cfg.Resolve(name)
		for _, e := range errs {
			issues = append(issues, Issue{Scope: name, Msg: e})
		}
		if resolved.Tree == nil {
			continue
		}
		issues = append(issues, validateResolved(name, resolved.Tree, cfg.Global.RuntimeDir(), defined, services, effectiveBackend(cfg))...)
	}
	return issues
}

func validateMounts(cfg *Config) []Issue {
	var issues []Issue
	paths := map[string]string{}
	for _, name := range cfg.MountNames {
		if name == "" {
			continue
		}
		resolved, errs := cfg.ResolveMount(name)
		for _, e := range errs {
			issues = append(issues, Issue{Scope: "mount " + name, Msg: e})
		}
		if resolved.Tree == nil {
			continue
		}
		issues = append(issues, validateMount(name, resolved.Tree)...)
		path := filepath.Clean(cfgval.String(resolved.Tree["path"]))
		if path != "." && path != "" {
			if prev := paths[path]; prev != "" && prev != name {
				issues = append(issues, Issue{Scope: "mount " + name, Msg: fmt.Sprintf("path %q is already used by mount %q", path, prev)})
			} else {
				paths[path] = name
			}
		}
	}
	return issues
}

func validateMount(name string, tree map[string]any) []Issue {
	var issues []Issue
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: "mount " + name, Msg: fmt.Sprintf(format, args...)})
	}

	allowed := set("kind", "name", "display_name", "description", "category", "path", "refcount", "umount", "stop_policy", "variables", "os")
	for _, key := range slices.Sorted(maps.Keys(tree)) {
		if _, ok := allowed[key]; !ok {
			add("key %q is not supported for kind: mount", key)
		}
	}

	path := cfgval.String(tree["path"])
	if path == "" {
		add("path is required")
	} else if !filepath.IsAbs(path) {
		add("path %q must be an absolute path", path)
	}
	if v, present := tree["refcount"]; present {
		if _, ok := v.(bool); !ok {
			add("refcount must be true or false")
		}
	}

	umount, _ := tree["umount"].(map[string]any)
	if _, present := tree["umount"]; present && umount == nil {
		add("umount must be a mapping")
	}
	allowSIGKILL := false
	if umount != nil {
		allowedUmount := set("term_timeout", "kill_timeout", "allow_sigkill", "allow_lazy")
		for _, key := range slices.Sorted(maps.Keys(umount)) {
			if _, ok := allowedUmount[key]; !ok {
				add("umount key %q is not one of term_timeout, kill_timeout, allow_sigkill, allow_lazy", key)
			}
		}
		for _, field := range []string{"term_timeout", "kill_timeout"} {
			if v, present := umount[field]; present && !isPositiveDuration(cfgval.String(v)) {
				add("umount.%s %q must be a valid positive duration", field, cfgval.String(v))
			}
		}
		for _, field := range []string{"allow_sigkill", "allow_lazy"} {
			if v, present := umount[field]; present {
				b, ok := v.(bool)
				if !ok {
					add("umount.%s must be true or false", field)
				}
				if field == "allow_sigkill" && ok && b {
					allowSIGKILL = true
				}
			}
		}
	}

	if sp, ok := tree["stop_policy"].(map[string]any); ok {
		force, _ := sp["force_kill"].(bool)
		if force {
			allowSIGKILL = true
		}
	} else if _, present := tree["stop_policy"]; present {
		add("stop_policy must be a mapping")
	}
	validateStopPolicy(tree, add)
	if allowSIGKILL {
		sp, _ := tree["stop_policy"].(map[string]any)
		_, hasKoi := sp["kill_only_if"].(map[string]any)
		if !hasKoi {
			add("umount.allow_sigkill=true requires stop_policy.kill_only_if")
		}
	}

	for _, e := range validateVariableValues(collectVariables(tree)) {
		add("variables: %s", e)
	}
	return issues
}

// effectiveBackend returns the init backend validation should assume: an explicit
// engine.backend when set, otherwise the host-detected init (${init}).
func effectiveBackend(cfg *Config) string {
	if engine, ok := cfg.Global.Raw["engine"].(map[string]any); ok {
		if backend := cfgval.String(engine["backend"]); backend != "" && backend != "auto" {
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
	validateCheckSection(tree, "postflight", locksDir, add)
	validateProcesses(tree, add)
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
