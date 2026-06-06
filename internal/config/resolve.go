package config

import (
	"fmt"
	"strings"
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

	vars := collectVariables(merged)
	errs := validateVariableValues(vars)
	injectBuiltinVariables(vars, name, merged)
	expanded, expErrs := expandTree(merged, vars)
	errs = append(errs, expErrs...)
	errs = append(errs, c.expandRestartOnChange(expanded)...)

	return Resolved{Name: name, Tree: expanded}, errs
}

// injectBuiltinVariables makes the document's identity available for ${...}
// expansion: ${name} (the resolved service name) and ${display_name} (the
// display_name field, falling back to name). They let profiles parameterize
// human-facing strings — e.g. message: "${display_name} backup is running".
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
}

// expandRestartOnChange desugars a `restart_on_change: {libraries: [...]}` block
// into one remediation rule per library that restarts the service when the
// library file changes. Each named library is resolved to its file via the
// matching library profile, so the generated `changed:` condition carries a
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
	for _, lib := range stringList(roc["libraries"]) {
		doc, ok := c.Profiles[lib]
		if !ok || doc.Category != CategoryLibrary {
			errs = append(errs, fmt.Sprintf("restart_on_change references %q, which is not a library profile", lib))
			continue
		}
		path := profileBinary(doc.Body)
		if path == "" {
			errs = append(errs, fmt.Sprintf("library %q has no binary to watch", lib))
			continue
		}
		libraries["restart-on-change-"+lib] = map[string]any{
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

// ResolveProfile expands a profile's own body — no service merge — so its
// concrete values (notably the binary path and preflight commands) can be
// inspected directly, as the `apps` command does. ${name} and ${display_name}
// are available; the returned errors mirror Resolve's.
func (c *Config) ResolveProfile(name string) (Resolved, []string) {
	doc, ok := c.Profiles[name]
	if !ok {
		return Resolved{Name: name}, []string{fmt.Sprintf("unknown profile %q", name)}
	}
	body := stripMeta(doc.Body)
	vars := collectVariables(body)
	errs := validateVariableValues(vars)
	injectBuiltinVariables(vars, name, body)
	expanded, expErrs := expandTree(body, vars)
	return Resolved{Name: name, Tree: expanded}, append(errs, expErrs...)
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
	if clone := scalarString(doc.Body["clone"]); clone != "" {
		src, err := c.mergedService(clone, append(chain, name))
		if err != nil {
			return nil, err
		}
		merged = src
	} else {
		merged = c.defaultsPerService()
		if uses := scalarString(doc.Body["uses"]); uses != "" {
			profile, ok := c.Profiles[uses]
			if !ok {
				return nil, fmt.Errorf("service %q uses unknown profile %q", name, uses)
			}
			merged = mergeMaps(merged, stripMeta(profile.Body))
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
