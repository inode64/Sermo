package config

import (
	"strings"
	"testing"
)

// validateRawGlobal builds a minimal-but-valid global config (Validate always
// requires defaults.policy.cooldown via validateGlobal) carrying the given raw
// sections, then returns all issues. Tests below filter to watch issues by
// substring since every issue is Scope "global".
func validateRawGlobal(t *testing.T, global map[string]any) []Issue {
	t.Helper()
	cfg := &Config{Global: Global{
		Raw:      global,
		Defaults: map[string]any{"policy": map[string]any{"cooldown": "5m"}},
	}}
	return Validate(cfg) // package function, not a method
}

// watchIssues returns only the issues whose message mentions "watches." so the
// always-present global checks (cooldown, etc.) don't mask watch validation.
func watchIssues(issues []Issue) []Issue {
	var out []Issue
	for _, i := range issues {
		if strings.Contains(i.Msg, "watches.") {
			out = append(out, i)
		}
	}
	return out
}

func TestValidateWatchesGood(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"disk-root": map[string]any{
				"check": map[string]any{"type": "disk", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/alert.sh"}}},
			},
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}
}

func TestValidateWatchesBad(t *testing.T) {
	cases := map[string]map[string]any{
		"unknown type": {"check": map[string]any{"type": "bogus"}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"disk no path": {"check": map[string]any{"type": "disk", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"bad op":       {"check": map[string]any{"type": "disk", "path": "/", "used_pct": map[string]any{"op": "=>", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"empty cmd":    {"check": map[string]any{"type": "disk", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{}}}},
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			issues := watchIssues(validateRawGlobal(t, map[string]any{"watches": map[string]any{"w": w}}))
			if len(issues) == 0 {
				t.Fatalf("%s: expected a watch issue", name)
			}
		})
	}
}

func TestValidateWatchesMessageMentionsName(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{"disk-root": map[string]any{"check": map[string]any{"type": "disk"}}},
	})
	joined := ""
	for _, i := range watchIssues(issues) {
		joined += i.Msg
	}
	if !strings.Contains(joined, "disk-root") {
		t.Fatalf("issue should name the watch: %v", issues)
	}
}
