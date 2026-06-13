package config

import (
	"slices"
	"testing"
)

func TestServiceCandidates(t *testing.T) {
	perInit := map[string]any{"service": map[string]any{
		"systemd": []any{"nginx.service"},
		"openrc":  []any{"nginx"},
	}}
	cases := []struct {
		name      string
		tree      map[string]any
		backend   string
		wantCands []string
		wantTrust bool
	}{
		{"scalar is a trusted single candidate", map[string]any{"service": "nginx"}, "systemd", []string{"nginx"}, true},
		{"per-init picks the backend list (not trusted)", perInit, "systemd", []string{"nginx.service"}, false},
		{"per-init for the other backend", perInit, "openrc", []string{"nginx"}, false},
		{"per-init with no entry for the backend is unavailable", map[string]any{"service": map[string]any{"systemd": []any{"x"}}}, "openrc", nil, false},
		{"legacy name form is trusted", map[string]any{"service": map[string]any{"name": "redis"}}, "systemd", []string{"redis"}, true},
		{"empty scalar falls back to the name", map[string]any{"service": ""}, "systemd", []string{"fallback"}, true},
		{"no service key falls back to the name", map[string]any{}, "systemd", []string{"fallback"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cands, trust := ServiceCandidates(tc.tree, tc.backend, "fallback")
			if !slices.Equal(cands, tc.wantCands) {
				t.Fatalf("candidates = %v, want %v", cands, tc.wantCands)
			}
			if trust != tc.wantTrust {
				t.Fatalf("trust = %v, want %v", trust, tc.wantTrust)
			}
		})
	}
}
