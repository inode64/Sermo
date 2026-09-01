package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"sermo/internal/cfgval"
)

func underDir(path, dir string) bool {
	clean := filepath.Clean(path)
	dir = filepath.Clean(dir)
	return clean == dir || strings.HasPrefix(clean, dir+string(filepath.Separator))
}

func set(values ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, v := range values {
		out[v] = struct{}{}
	}
	return out
}

// walkScalars visits every scalar leaf in the tree (skipping the `variables`
// section, whose raw values are not target-typed fields), reporting the dotted
// path, the leaf key and its stringified value.
func walkScalars(tree map[string]any, visit func(path, key, value string)) {
	for k, v := range tree {
		if k == sectionVariables {
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
			walkScalarValue(fmt.Sprintf(validationListIndexFormat, path, i), key, e, visit)
		}
	default:
		visit(path, key, cfgval.String(t))
	}
}

func isPositiveDuration(s string) bool {
	return isDuration(s, false)
}

func isNonNegativeDuration(s string) bool {
	return isDuration(s, true)
}

// isDuration reports whether s parses as a duration that is either >0 (when
// !allowZero) or >=0. Centralizes the repeated Parse+check to remove dupe.
func isDuration(s string, allowZero bool) bool {
	d, ok := cfgval.ParseDuration(s)
	if !ok {
		return false
	}
	if allowZero {
		return d >= 0
	}
	return d > 0
}
