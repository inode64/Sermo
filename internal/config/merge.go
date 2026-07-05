package config

import "sermo/internal/cfgval"

// namedSections are maps keyed by entry name where `enabled:false`/`delete:true`
// apply.
var namedSections = []string{"checks", "preflight", "processes", "rules"}

// mergeMaps merges src on top of dst and returns a new map. Scalars and lists
// overwrite; nested maps merge recursively. Inputs are not mutated.
func mergeMaps(dst, src map[string]any) map[string]any {
	out := cloneMap(dst)
	for k, sv := range src {
		if dv, ok := out[k]; ok {
			dm, dIsMap := dv.(map[string]any)
			sm, sIsMap := sv.(map[string]any)
			if dIsMap && sIsMap {
				out[k] = mergeMaps(dm, sm)
				continue
			}
		}
		out[k] = deepCopy(sv)
	}
	return out
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
			if cfgval.Bool(entry["delete"]) {
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
