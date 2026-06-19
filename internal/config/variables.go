package config

import (
	"fmt"
	"maps"
	"os"
	"regexp"
	"sermo/internal/cfgval"
	"slices"
	"strings"
)

// varRef matches a single ${name} reference. There is no escape syntax in the
// MVP: `${` always begins a reference (section 10).
var varRef = regexp.MustCompile(`\$\{([^}]*)\}`)

// collectVariables reads the merged `variables` section into a flat string map.
// Values are stringified (a YAML int like `port: 8080` becomes "8080"). A
// list-valued variable is treated as candidate paths and resolves to the first
// one that exists on the filesystem (see firstExistingPath). Resource preflight
// entries may have already narrowed a list to the candidate that matches their
// type (binary, file, socket or pidfile); collectVariables just consumes the
// resulting variables map.
func collectVariables(tree map[string]any) map[string]string {
	return collectVariablesForKind(tree, cfgval.String(tree["kind"]))
}

func collectVariablesForKind(tree map[string]any, _ string) map[string]string {
	raw, ok := tree["variables"].(map[string]any)
	vars := map[string]string{}
	if ok {
		vars = make(map[string]string, len(raw))
		for k, v := range raw {
			if list, ok := v.([]any); ok {
				vars[k] = expandEnvString(firstExistingPath(list))
				continue
			}
			// Resolve ${env:...} in a variable value here (before nested-variable
			// validation) so a variable can hold a secret from the environment.
			vars[k] = expandEnvString(cfgval.String(v))
		}
	}
	return vars
}

// firstExistingPath resolves a list-valued variable to the first candidate path
// that exists on the filesystem, stopping at the first hit. This lets a daemon
// list alternative locations for the same binary (e.g. /lib vs /usr/lib) and
// bind the variable to whichever is present, so the rest of the document can
// reference it via ${name}. If none exist, it falls back to the first candidate
// so the value stays well-formed and downstream preflight checks report it as
// missing rather than expanding to an empty string.
func firstExistingPath(candidates []any) string {
	return firstExistingStringPath(cfgval.StringList(candidates))
}

func firstExistingStringPath(candidates []string) string {
	var first string
	for _, p := range candidates {
		p = expandEnvString(p)
		if p == "" {
			continue
		}
		if first == "" {
			first = p // first non-empty candidate, the fallback when none exist
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return first
}

// DocumentBinary returns the document's configured binary variable. It is used
// by catalog inspection and version discovery paths that need to read raw catalog
// documents before full service resolution.
func DocumentBinary(tree map[string]any) string {
	return firstExistingStringPath(documentBinaryCandidates(tree))
}

func documentBinaryCandidates(tree map[string]any) []string {
	vars, _ := tree["variables"].(map[string]any)
	if vars == nil {
		return nil
	}
	return cfgval.StringList(vars["binary"])
}

// validateVariableValues rejects variable values that themselves contain
// ${...} (no nested variables in the MVP, section 10).
func validateVariableValues(vars map[string]string) []string {
	var errs []string
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if varRef.MatchString(vars[name]) {
			errs = append(errs, fmt.Sprintf("variable %s references another variable in its value %q (nested variables are not allowed)", name, vars[name]))
		}
	}
	return errs
}

// expandTree substitutes ${var} references across every string in the tree,
// once, leaving the `variables` section itself untouched. It returns the
// expanded tree and a list of errors for undefined references, each naming the
// dotted path where the reference appeared.
func expandTree(tree map[string]any, vars map[string]string) (map[string]any, []string) {
	var errs []string
	out := make(map[string]any, len(tree))
	for k, v := range tree {
		if k == "variables" {
			out[k] = v
			continue
		}
		out[k] = expandValue(v, vars, k, &errs)
	}
	return out, errs
}

func expandValue(v any, vars map[string]string, path string, errs *[]string) any {
	switch t := v.(type) {
	case string:
		return expandString(t, vars, path, errs)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = expandValue(e, vars, path+"."+k, errs)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = expandValue(e, vars, fmt.Sprintf("%s[%d]", path, i), errs)
		}
		return out
	default:
		return t
	}
}

// runtimeVars are substituted by the worker when an event is emitted, not during
// resolution. expandString leaves them as literal ${...} (without erroring) so a
// rule message can reference the firing event's context.
var runtimeVars = map[string]bool{"date": true, "event": true, "action": true}

func expandString(s string, vars map[string]string, path string, errs *[]string) string {
	return varRef.ReplaceAllStringFunc(s, func(match string) string {
		name := strings.TrimSpace(varRef.FindStringSubmatch(match)[1])
		if rest, ok := strings.CutPrefix(name, "env:"); ok {
			return resolveEnvRef(rest) // ${env:NAME} -> environment, never an error
		}
		if val, ok := vars[name]; ok {
			return val
		}
		if runtimeVars[name] {
			return match // resolved at emit time by the worker
		}
		*errs = append(*errs, fmt.Sprintf("variable ${%s} used in %s but not defined", name, path))
		return match
	})
}

// resolveEnvRef resolves the body of an ${env:...} reference (the text after
// "env:") from the process environment, with an optional shell-style default:
// ${env:NAME} or ${env:NAME:-fallback}. An unset or empty variable yields the
// default ("" when none), never an error — secrets need not be present when the
// config is merely validated.
func resolveEnvRef(ref string) string {
	name, def := ref, ""
	if i := strings.Index(ref, ":-"); i >= 0 {
		name, def = ref[:i], ref[i+2:]
	}
	if v := os.Getenv(strings.TrimSpace(name)); v != "" {
		return v
	}
	return def
}

// expandEnvString replaces only ${env:...} references in s, leaving every other
// ${...} untouched. Used to resolve secrets in the global config (which has no
// per-service variables) and inside variable values.
func expandEnvString(s string) string {
	if !strings.Contains(s, "${env:") {
		return s
	}
	return varRef.ReplaceAllStringFunc(s, func(match string) string {
		name := strings.TrimSpace(varRef.FindStringSubmatch(match)[1])
		if rest, ok := strings.CutPrefix(name, "env:"); ok {
			return resolveEnvRef(rest)
		}
		return match
	})
}

// expandEnvTree resolves ${env:...} references across every string in a tree,
// recursively and in place, returning the same tree. Used for the global config.
func expandEnvTree(v any) any {
	switch t := v.(type) {
	case string:
		return expandEnvString(t)
	case map[string]any:
		for k, e := range t {
			t[k] = expandEnvTree(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = expandEnvTree(e)
		}
		return t
	default:
		return t
	}
}
