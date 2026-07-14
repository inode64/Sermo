package checks

import (
	"context"
	"testing"
)

func TestLevelChecks(t *testing.T) {
	tests := []struct {
		name        string
		firing      Check
		healthy     Check
		unavailable Check
		dataKey     string
		value       uint64
		entry       map[string]any
		deps        Deps
	}{
		{
			name:        "entropy",
			firing:      entropyCheck{base: base{name: "e"}, op: "<", value: 200, sampler: func() (uint64, bool) { return 120, true }},
			healthy:     entropyCheck{base: base{name: "e"}, op: "<", value: 200, sampler: func() (uint64, bool) { return 3000, true }},
			unavailable: entropyCheck{base: base{name: "e"}, op: "<", value: 200, sampler: func() (uint64, bool) { return 0, false }},
			dataKey:     DataKeyAvail,
			value:       120,
			entry:       map[string]any{"type": "entropy", "avail": map[string]any{"op": "<", "value": 200}},
			deps:        Deps{EntropySampler: func() (uint64, bool) { return 100, true }},
		},
		{
			name:        "zombies",
			firing:      zombieCheck{base: base{name: "z"}, op: ">", value: 20, sampler: func() (uint64, bool) { return 35, true }},
			healthy:     zombieCheck{base: base{name: "z"}, op: ">", value: 20, sampler: func() (uint64, bool) { return 3, true }},
			unavailable: zombieCheck{base: base{name: "z"}, op: ">", value: 0, sampler: func() (uint64, bool) { return 0, false }},
			dataKey:     DataKeyZombies,
			value:       35,
			entry:       map[string]any{"type": "zombies", "count": map[string]any{"op": ">", "value": 10}},
			deps:        Deps{ZombieSampler: func() (uint64, bool) { return 50, true }},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if result := tc.firing.Run(context.Background()); !result.OK || result.Data[tc.dataKey] != tc.value {
				t.Fatalf("firing result = %+v, want triggered value %d", result, tc.value)
			}
			if result := tc.healthy.Run(context.Background()); result.OK {
				t.Fatalf("healthy result = %+v, want not triggered", result)
			}
			if result := tc.unavailable.Run(context.Background()); result.OK {
				t.Fatalf("unavailable result = %+v, want not triggered", result)
			}
			built, warns := Build(map[string]any{tc.name: tc.entry}, tc.deps)
			if len(warns) != 0 || len(built) != 1 || !built[0].Check.Run(context.Background()).OK {
				t.Fatalf("built check = %+v warns=%v, want one firing check", built, warns)
			}
			if _, warns := Build(map[string]any{tc.name: map[string]any{"type": tc.name}}, Deps{}); len(warns) == 0 {
				t.Fatal("check without its predicate should warn")
			}
		})
	}
}
