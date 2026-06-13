package app

import "testing"

func TestUsesMetrics(t *testing.T) {
	cases := []struct {
		name        string
		tree        map[string]any
		wantService bool
		wantSystem  bool
	}{
		{
			name:        "service-scope metric check",
			tree:        map[string]any{"checks": map[string]any{"c": map[string]any{"type": "metric", "name": "cpu"}}},
			wantService: true,
		},
		{
			name:        "metric check without scope defaults to service",
			tree:        map[string]any{"checks": map[string]any{"c": map[string]any{"type": "metric"}}},
			wantService: true,
		},
		{
			name: "system metric nested in a rule and-condition",
			tree: map[string]any{"rules": map[string]any{"r": map[string]any{"if": map[string]any{
				"and": []any{map[string]any{"metric": map[string]any{"scope": "system", "name": "load1"}}},
			}}}},
			wantSystem: true,
		},
		{
			name: "system metric under a not-condition",
			tree: map[string]any{"rules": map[string]any{"r": map[string]any{"if": map[string]any{
				"not": map[string]any{"metric": map[string]any{"scope": "system", "name": "load1"}},
			}}}},
			wantSystem: true,
		},
		{
			name: "no metric references",
			tree: map[string]any{"checks": map[string]any{"c": map[string]any{"type": "tcp"}}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, sys := usesMetrics(c.tree)
			if svc != c.wantService || sys != c.wantSystem {
				t.Fatalf("usesMetrics = (service=%v, system=%v), want (%v, %v)", svc, sys, c.wantService, c.wantSystem)
			}
		})
	}
}
