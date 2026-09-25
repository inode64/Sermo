package config

import (
	"maps"
	"slices"

	"sermo/internal/cfgval"
	"sermo/internal/emission"
)

func validateEmission(tree map[string]any, path string, add addFunc) {
	raw, present := tree[emission.Section]
	if !present {
		return
	}
	m, ok := raw.(map[string]any)
	if !ok {
		add(validationMappingFormat, path)
		return
	}

	for _, key := range slices.Sorted(maps.Keys(m)) {
		if _, ok := emissionKeys[key]; !ok {
			add("%s.%s is not supported", path, key)
			continue
		}
		if mode := cfgval.String(m[key]); !emission.ValidMode(mode) {
			add("%s.%s %q is not one of %s", path, key, mode, emission.ModeSummary)
		}
	}
}

var emissionKeys = set(emission.KeyEvents, emission.KeyNotify)
