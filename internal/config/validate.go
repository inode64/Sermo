package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"time"
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
// Covered now: document kind/name presence, kind validity, name uniqueness,
// uses/clone resolution, clone cycles, variable existence/nesting/expansion,
// backend values, paths.locks rejection, paths.runtime absoluteness, security
// toggles, defaults and per-service policy.cooldown positivity, and
// port/expect_status range after expansion. Deeper rule/check cross-reference
// rules from section 30 are not yet enforced.
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
		if backend := scalarString(engine["backend"]); !isValidBackend(backend) {
			add("engine.backend %q is not one of auto, systemd, openrc", backend)
		}
	}

	if paths, ok := raw["paths"].(map[string]any); ok {
		if _, present := paths["locks"]; present {
			add("paths.locks is not supported in the MVP; runtime locks derive from paths.runtime")
		}
		if runtime := scalarString(paths["runtime"]); runtime != "" && !filepath.IsAbs(runtime) {
			add("paths.runtime %q must be an absolute directory", runtime)
		}
	}

	if security, ok := raw["security"].(map[string]any); ok {
		for _, key := range rejectedSecurityToggles {
			if _, present := security[key]; present {
				add("security.%s is a hard safety invariant and cannot be configured", key)
			}
		}
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
	profileCount := map[string]int{}
	serviceCount := map[string]int{}

	for _, doc := range cfg.docs {
		scope := documentScope(doc)
		switch doc.Kind {
		case kindProfile, kindService:
		case "":
			issues = append(issues, Issue{Scope: scope, Msg: "document has no kind (expected profile or service)"})
			continue
		default:
			issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("unknown kind %q (expected profile or service)", doc.Kind)})
			continue
		}
		if doc.Name == "" {
			issues = append(issues, Issue{Scope: scope, Msg: "document has no name"})
			continue
		}
		if doc.Kind == kindProfile {
			profileCount[doc.Name]++
		} else {
			serviceCount[doc.Name]++
		}
	}

	for _, name := range sortedKeys(profileCount) {
		if profileCount[name] > 1 {
			issues = append(issues, Issue{Scope: "profile " + name, Msg: "duplicate profile name"})
		}
	}
	for _, name := range sortedKeys(serviceCount) {
		if serviceCount[name] > 1 {
			issues = append(issues, Issue{Scope: "service " + name, Msg: "duplicate service name"})
		}
	}
	return issues
}

func validateServices(cfg *Config) []Issue {
	var issues []Issue
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
		issues = append(issues, validateResolved(name, resolved.Tree)...)
	}
	return issues
}

func validateResolved(name string, tree map[string]any) []Issue {
	var issues []Issue
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: name, Msg: fmt.Sprintf(format, args...)})
	}

	if backend := scalarString(tree["backend"]); !isValidBackend(backend) {
		add("backend %q is not one of auto, systemd, openrc", backend)
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
			if n, err := strconv.Atoi(value); err != nil || n < 100 || n > 599 {
				add("%s = %q must resolve to a valid HTTP status", path, value)
			}
		}
	})

	return issues
}

func defaultsCooldown(defaults map[string]any) (string, bool) {
	policy, ok := defaults["policy"].(map[string]any)
	if !ok {
		return "", false
	}
	v, present := policy["cooldown"]
	if !present {
		return "", false
	}
	return scalarString(v), true
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
	return scalarString(v), true
}

// walkScalars visits every scalar leaf in the tree (skipping the `variables`
// section, whose raw values are not target-typed fields), reporting the dotted
// path, the leaf key and its stringified value.
func walkScalars(tree map[string]any, visit func(path, key, value string)) {
	for k, v := range tree {
		if k == "variables" {
			continue
		}
		walkScalarValue(k, k, v, visit)
	}
}

func walkScalarValue(path, key string, v any, visit func(path, key, value string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			walkScalarValue(path+"."+k, k, e, visit)
		}
	case []any:
		for i, e := range t {
			walkScalarValue(fmt.Sprintf("%s[%d]", path, i), key, e, visit)
		}
	default:
		visit(path, key, scalarString(t))
	}
}

func isValidBackend(b string) bool {
	_, ok := validBackends[b]
	return ok
}

func isPositiveDuration(s string) bool {
	d, err := time.ParseDuration(s)
	return err == nil && d > 0
}

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
