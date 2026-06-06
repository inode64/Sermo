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
