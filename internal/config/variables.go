package config

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// varRef matches a single ${name} reference. There is no escape syntax in the
// MVP: `${` always begins a reference (section 10).
var varRef = regexp.MustCompile(`\$\{([^}]*)\}`)

// collectVariables reads the merged `variables` section into a flat string map.
// Values are stringified (a YAML int like `port: 8080` becomes "8080").
func collectVariables(tree map[string]any) map[string]string {
	raw, ok := tree["variables"].(map[string]any)
	if !ok {
		return map[string]string{}
	}
	vars := make(map[string]string, len(raw))
	for k, v := range raw {
		vars[k] = scalarString(v)
	}
	return vars
}

// validateVariableValues rejects variable values that themselves contain
// ${...} (no nested variables in the MVP, section 10).
func validateVariableValues(vars map[string]string) []string {
	var errs []string
	for _, name := range sortedKeys(vars) {
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

func expandString(s string, vars map[string]string, path string, errs *[]string) string {
	return varRef.ReplaceAllStringFunc(s, func(match string) string {
		name := strings.TrimSpace(varRef.FindStringSubmatch(match)[1])
		if val, ok := vars[name]; ok {
			return val
		}
		*errs = append(*errs, fmt.Sprintf("variable ${%s} used in %s but not defined", name, path))
		return match
	})
}

// scalarString renders a YAML scalar as the string Sermo uses for variables and
// FlexInt-style fields.
func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
