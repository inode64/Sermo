package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sermo/internal/cfgval"
	"strings"
)

// keyEnableIf is the per-entry gate key (and its label/path prefix in diagnostics).
const keyEnableIf = "enable_if"

const (
	enableIfEntryPathDepth = 2

	keyEnableIfFile     = "file"
	keyEnableIfKey      = "key"
	keyEnableIfContains = "contains"
	keyEnableIfEquals   = "equals"
	keyEnableIfMatches  = "matches"

	enableIfPredicateSummary = keyEnableIfContains + ", " + keyEnableIfEquals + " or " + keyEnableIfMatches
)

var (
	enableIfSections = set(sectionChecks, sectionPreflight, sectionProcesses, sectionWatches)
	enableIfKeys     = set(keyEnableIfFile, keyEnableIfKey, keyEnableIfContains, keyEnableIfEquals, keyEnableIfMatches)
)

func pruneEnableIf(v any, path []string) any {
	switch t := v.(type) {
	case map[string]any:
		return pruneEnableIfMap(t, path)
	case []any:
		for i := range t {
			t[i] = pruneEnableIf(t[i], path)
		}
		return t
	default:
		return t
	}
}

func pruneEnableIfMap(tree map[string]any, path []string) map[string]any {
	out := make(map[string]any, len(tree))
	for key, value := range tree {
		childPath := appendPath(path, key)
		if child, ok := value.(map[string]any); ok {
			if spec, has := child[keyEnableIf]; has {
				if !enableIfAllowedAt(childPath) {
					out[key] = pruneEnableIfMap(child, childPath)
					continue
				}
				if !enableIfHolds(spec) {
					continue // predicate failed: drop the optional branch
				}
				child = cloneMap(child)
				delete(child, keyEnableIf)
				out[key] = pruneEnableIfMap(child, childPath)
				continue
			}
		}
		out[key] = pruneEnableIf(value, childPath)
	}
	return out
}

func appendPath(path []string, key string) []string {
	out := make([]string, 0, len(path)+1)
	out = append(out, path...)
	out = append(out, key)
	return out
}

func enableIfAllowedAt(path []string) bool {
	if len(path) != enableIfEntryPathDepth {
		return false
	}
	_, ok := enableIfSections[path[0]]
	return ok
}

func validateEnableIfTree(tree map[string]any, add addFunc) {
	walkEnableIf(tree, nil, add)
}

func walkEnableIf(v any, path []string, add addFunc) {
	switch t := v.(type) {
	case map[string]any:
		if spec, has := t[keyEnableIf]; has {
			label := strings.Join(path, ".")
			if label == "" {
				label = keyEnableIf
			}
			if !enableIfAllowedAt(path) {
				add("%s.enable_if is only supported on entries under checks, preflight, processes or watches", label)
			}
			validateEnableIfSpec(label+".enable_if", spec, add)
		}
		for key, child := range t {
			walkEnableIf(child, appendPath(path, key), add)
		}
	case []any:
		for i, child := range t {
			walkEnableIf(child, appendPath(path, fmt.Sprintf("[%d]", i)), add)
		}
	}
}

func validateEnableIfSpec(path string, spec any, add addFunc) {
	m, ok := spec.(map[string]any)
	if !ok {
		add(validationMappingFormat, path)
		return
	}
	for key := range m {
		if _, ok := enableIfKeys[key]; !ok {
			add("%s.%s is not supported; enable_if accepts file, key and one of %s", path, key, enableIfPredicateSummary)
		}
	}
	file := cfgval.String(m[keyEnableIfFile])
	if file == "" {
		add("%s.file is required", path)
	} else if !filepath.IsAbs(file) {
		add("%s.file %q must be absolute", path, file)
	}
	if cfgval.String(m[keyEnableIfKey]) == "" {
		add("%s.key is required", path)
	}
	if predicates := validateEnableIfPredicates(path, m, add); predicates != 1 {
		add("%s must define exactly one of %s", path, enableIfPredicateSummary)
	}
}

func validateEnableIfPredicates(path string, m map[string]any, add addFunc) int {
	predicates := 0
	if _, has := m[keyEnableIfContains]; has {
		predicates++
		if cfgval.String(m[keyEnableIfContains]) == "" {
			add("%s.contains must be non-empty", path)
		}
	}
	if _, has := m[keyEnableIfEquals]; has {
		predicates++
	}
	if _, has := m[keyEnableIfMatches]; has {
		predicates++
		pat := cfgval.String(m[keyEnableIfMatches])
		if pat == "" {
			add("%s.matches must be non-empty", path)
		} else if _, err := regexp.Compile(pat); err != nil {
			add("%s.matches is not a valid regex: %v", path, err)
		}
	}
	return predicates
}

// enableIfHolds evaluates an enable_if predicate against a distro config file.
// It is fail-safe: a malformed spec, an unreadable file, or an absent key all
// yield false, so an optional component stays disabled unless explicitly turned
// on. Predicates: `contains` (substring), `equals` (exact), `matches` (regex).
func enableIfHolds(spec any) bool {
	m, ok := spec.(map[string]any)
	if !ok {
		return false
	}
	valid := true
	validateEnableIfSpec(keyEnableIf, spec, func(string, ...any) {
		valid = false
	})
	if !valid {
		return false
	}
	file, key := cfgval.String(m[keyEnableIfFile]), cfgval.String(m[keyEnableIfKey])
	if file == "" || key == "" {
		return false
	}
	val, ok := confdValue(file, key)
	if !ok {
		return false
	}
	return enableIfPredicateMatches(m, val)
}

func enableIfPredicateMatches(m map[string]any, val string) bool {
	if want := cfgval.String(m[keyEnableIfContains]); want != "" {
		return strings.Contains(val, want)
	}
	if want, has := m[keyEnableIfEquals]; has {
		return val == cfgval.String(want)
	}
	if pat := cfgval.String(m[keyEnableIfMatches]); pat != "" {
		re, err := regexp.Compile(pat)
		return err == nil && re.MatchString(val)
	}
	return false
}
