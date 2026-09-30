package config

import (
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/rules"
)

// namedSections are maps keyed by entry name where `enabled:false`/`delete:true`
// apply.
var namedSections = []string{sectionChecks, sectionPreflight, sectionProcesses, rules.SectionRules, sectionWatches}

// mergeMaps merges src on top of dst and returns a new map. Scalars and lists
// overwrite; nested maps merge recursively. Inputs are not mutated.
//
// One key follows its owner rather than the merge: a check's `levels:` restate
// the thresholds of the check's type, so an override that changes the `type:`
// drops the inherited tiers unless it declares its own.
func mergeMaps(dst, src map[string]any) map[string]any {
	out := make(map[string]any, len(dst))
	retyped := overridesType(dst, src)
	for k, dv := range dst {
		if _, overridden := src[k]; !overridden && (!retyped || k != checks.CheckKeyLevels) {
			out[k] = deepCopy(dv)
		}
	}
	for k, sv := range src {
		if retyped && k == checks.CheckKeyLevels {
			out[k] = deepCopy(sv) // tiers for the new type replace the old ones
			continue
		}
		if dm, sm, ok := mergeableMaps(dst[k], sv); ok {
			out[k] = mergeMaps(dm, sm)
			continue
		}
		out[k] = deepCopy(sv)
	}
	return out
}

// overridesType reports an override that gives a typed entry another type.
func overridesType(dst, src map[string]any) bool {
	typ, typed := src[checks.CheckKeyType]
	return typed && dst[checks.CheckKeyType] != nil && cfgval.String(typ) != cfgval.String(dst[checks.CheckKeyType])
}

func mergeableMaps(dst, src any) (map[string]any, map[string]any, bool) {
	dm, dIsMap := dst.(map[string]any)
	sm, sIsMap := src.(map[string]any)
	return dm, sm, dIsMap && sIsMap
}

// applyDeletes drops entries marked `delete: true` from named sections after a
// merge. `enabled: false` entries are kept (disabled, not removed).
func applyDeletes(tree map[string]any) {
	for _, section := range namedSections {
		entries, ok := tree[section].(map[string]any)
		if !ok {
			continue
		}
		for name, raw := range entries {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if cfgval.Bool(entry[keyDelete]) {
				delete(entries, name)
			}
		}
	}
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = deepCopy(v)
	}
	return out
}

func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneMap(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = deepCopy(e)
		}
		return out
	default:
		return t
	}
}

func stringValues(values []string) []any {
	out := make([]any, len(values))
	for i, c := range values {
		out[i] = c
	}
	return out
}
