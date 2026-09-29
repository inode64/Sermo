package config

import (
	"fmt"
	"maps"
	"slices"

	"sermo/internal/cfgval"
	"sermo/internal/rules"
	"sermo/internal/strutil"
)

// set builds a membership set of the given keys through the shared helper.
func set(values ...string) map[string]struct{} {
	return strutil.Set(values)
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

// validateMappingSection distinguishes an absent optional section from one
// that would otherwise disappear silently because its shape is invalid.
func validateMappingSection(tree map[string]any, key string, add addFunc) map[string]any {
	raw, present := tree[key]
	if !present {
		return nil
	}
	section, ok := raw.(map[string]any)
	if !ok {
		add(validationMappingFormat, key)
	}
	return section
}

// serviceSectionErrors runs before expansion can add generated entries to a
// section and accidentally replace an operator's malformed declaration.
func serviceSectionErrors(tree map[string]any) []string {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	for _, section := range []string{sectionChecks, sectionPreflight, rules.SectionRules, sectionProcesses, sectionCommands} {
		validateMappingSection(tree, section, add)
	}
	return errs
}

// namedEntryFlagErrors rejects a non-boolean `enabled` or `delete` on an entry
// of a mergeable named section. YAML 1.2 reads `no` or "false" as a string,
// which cfgval.Disabled and applyDeletes treat as "keep it active", so such a
// typo would silently leave a check or remediation running. It runs on the
// merged tree, before expansion folds service watches into checks and rules.
func namedEntryFlagErrors(tree map[string]any) []string {
	var errs []string
	for _, section := range namedSections {
		entries, _ := tree[section].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(entries)) {
			entry, ok := entries[name].(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{keyEnabled, keyDelete} {
				if v, present := entry[key]; present {
					if _, isBool := v.(bool); !isBool {
						errs = append(errs, fmt.Sprintf(validationBooleanLiteralFormat, section+"."+name+"."+key))
					}
				}
			}
		}
	}
	return errs
}
