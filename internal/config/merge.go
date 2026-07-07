package config

import (
	"sermo/internal/cfgval"
	"sermo/internal/rules"
)

// namedSections are maps keyed by entry name where `enabled:false`/`delete:true`
// apply.
var namedSections = []string{sectionChecks, sectionPreflight, sectionProcesses, rules.SectionRules, sectionWatches}

// mergeMaps merges src on top of dst and returns a new map. Scalars and lists
// overwrite; nested maps merge recursively. Inputs are not mutated.
func mergeMaps(dst, src map[string]any) map[string]any {
	out := cloneMap(dst)
	for k, sv := range src {
		if dm, sm, ok := mergeableMaps(out[k], sv); ok {
			out[k] = mergeMaps(dm, sm)
			continue
		}
		out[k] = deepCopy(sv)
	}
	return out
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
