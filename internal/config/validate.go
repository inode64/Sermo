package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"sermo/internal/cfgval"
)

// Issue is a single validation finding, scoped to a document or "global".
type Issue struct {
	Scope string
	Msg   string
}

func (i Issue) String() string {
	return fmt.Sprintf("%s: %s", i.Scope, i.Msg)
}

var validBackends = map[string]struct{}{"": {}, "auto": {}, "systemd": {}, "openrc": {}}

// rejectedSecurityToggles are keys under `security:` that try to disable hard
// safety invariants and must never be honored (section 30).
var rejectedSecurityToggles = []string{
	"require_preflight_before_restart",
	"block_restart_on_active_lock",
	"allow_sigkill_by_default",
	"require_kill_selector",
}

// Validate runs the implemented subset of section 30 over a loaded config and
// returns every issue found. An empty slice means the configuration is valid as
// far as the current checks go.
//
// Covered: document kind/name presence and uniqueness, uses/clone resolution and
// cycles, variable existence/nesting/expansion, backend values, engine durations
// (including operation_timeout, the non-negative startup_delay) and
// max_parallel_checks/max_parallel_operations, paths.locks
// rejection, paths.runtime absoluteness,
// security toggles, policy.cooldown/max_actions/backoff, port/expect_status range,
// check/preflight/postflight entry schemas (type, optional, command array form,
// service/process states, metric grammar, file_exists not under the lock dir),
// stop_policy.force_kill/kill_only_if, service, and rules (type, if/then, action,
// guard/block constraints, for/within windows, and the condition tree: exactly
// one operator per node, check references, states, and metric scope/catalog with
// system metrics confined to alert rules — directly and indirectly through a
// metric check reference — and the metric threshold form matching the metric's
// available forms).
func Validate(cfg *Config) []Issue {
	var issues []Issue
	issues = append(issues, validateGlobal(cfg)...)
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
			add("paths.locks is not supported in the MVP; runtime locks derive from paths.runtime")
		}
		if runtime := cfgval.String(paths["runtime"]); runtime != "" && !filepath.IsAbs(runtime) {
			add("paths.runtime %q must be an absolute directory", runtime)
		}
		if stateDir := cfgval.String(paths["state"]); stateDir != "" && !filepath.IsAbs(stateDir) {
			add("paths.state %q must be an absolute directory", stateDir)
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
	validateNotifiers(notifiers, add)

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

	return issues
}

func validateDocuments(cfg *Config) []Issue {
	var issues []Issue
	// Duplicate names are detected per kind, so a daemon and an app may share a
	// name (e.g. the `apache` service and the `apache` app that owns its binary).
	counts := map[string]map[string]int{
		kindDaemon: {}, kindApp: {}, kindLibrary: {}, kindService: {},
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
		switch doc.Kind {
		case kindDaemon, kindApp, kindLibrary, kindService:
		case "":
			issues = append(issues, Issue{Scope: scope, Msg: "document has no kind (expected daemon, app, lib or service)"})
			continue
		default:
			issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("unknown kind %q (expected daemon, app, lib or service)", doc.Kind)})
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

	for _, kind := range []string{kindDaemon, kindApp, kindLibrary, kindService} {
		for _, name := range slices.Sorted(maps.Keys(counts[kind])) {
			if counts[kind][name] > 1 {
				issues = append(issues, Issue{Scope: kind + " " + name, Msg: "duplicate " + kind + " name"})
			}
		}
	}
	return issues
}

func validDocumentName(name string) bool {
	return name != "." && name != ".." && !strings.Contains(name, "/") && !strings.Contains(name, `\`)
}

func validateServices(cfg *Config) []Issue {
	var issues []Issue
	notifiers, _ := cfg.Global.Raw["notifiers"].(map[string]any)
	defined := notifierNames(notifiers)
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
		issues = append(issues, validateResolved(name, resolved.Tree, cfg.Global.RuntimeDir(), defined)...)
	}
	return issues
}

func validateResolved(name string, tree map[string]any, runtime string, notifiers map[string]struct{}) []Issue {
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
	validateServiceField(tree, add)
	validateCommands(tree, add)
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
