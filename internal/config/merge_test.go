package config

import (
	"testing"
)

func TestMergeMaps(t *testing.T) {
	dst := map[string]any{
		"a":       1,
		"shared":  map[string]any{"x": 1, "keep": "dst"},
		"dstonly": "d",
	}
	src := map[string]any{
		"a":       2,                                    // scalar override
		"shared":  map[string]any{"x": 9, "add": "src"}, // deep merge, not replace
		"srconly": "s",
		"list":    []any{"a", "b"},
	}
	out := mergeMaps(dst, src)

	if out["a"] != 2 || out["dstonly"] != "d" || out["srconly"] != "s" {
		t.Fatalf("override/dst-only/src-only wrong: %v", out)
	}
	sh := out["shared"].(map[string]any)
	if sh["x"] != 9 || sh["keep"] != "dst" || sh["add"] != "src" {
		t.Fatalf("nested map must deep-merge, got %v", sh)
	}

	// No aliasing: mutating the result must not reach back into src or dst.
	out["a"] = 999
	sh["x"] = 999
	out["list"].([]any)[0] = "MUT"
	if dst["a"] != 1 {
		t.Fatalf("dst scalar mutated: %v", dst["a"])
	}
	if src["shared"].(map[string]any)["x"] != 9 {
		t.Fatalf("src nested map mutated: %v", src["shared"])
	}
	if src["list"].([]any)[0] != "a" {
		t.Fatalf("src slice mutated: %v", src["list"])
	}
}

func TestApplyDeletesRemovesEntry(t *testing.T) {
	tree := map[string]any{
		"checks": map[string]any{
			"http": map[string]any{"delete": true},
			"tcp":  map[string]any{"type": "tcp"},
		},
	}
	applyDeletes(tree)
	checkEntries := tree["checks"].(map[string]any)
	if _, ok := checkEntries["http"]; ok {
		t.Errorf("http should be deleted")
	}
	if _, ok := checkEntries["tcp"]; !ok {
		t.Errorf("tcp should remain")
	}
}

func TestMergeMapsRetainedBranchesAreIndependent(t *testing.T) {
	dst := map[string]any{
		"retained": map[string]any{"list": []any{map[string]any{"value": "original"}}},
		"shared":   map[string]any{"keep": map[string]any{"value": "original"}},
		"replaced": map[string]any{"old": true},
	}
	src := map[string]any{"shared": map[string]any{"add": true}, "replaced": nil}
	out := mergeMaps(dst, src)
	out["retained"].(map[string]any)["list"].([]any)[0].(map[string]any)["value"] = "changed"
	out["shared"].(map[string]any)["keep"].(map[string]any)["value"] = "changed"
	if got := dst["retained"].(map[string]any)["list"].([]any)[0].(map[string]any)["value"]; got != "original" {
		t.Fatalf("retained branch aliases input: %v", got)
	}
	if got := dst["shared"].(map[string]any)["keep"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("merged branch aliases input: %v", got)
	}
	if out["replaced"] != nil {
		t.Fatalf("explicit nil must replace the old map: %v", out["replaced"])
	}
}

// A check's levels restate its type's thresholds: an override that changes the
// type drops the inherited tiers, or replaces them with its own, while one that
// keeps the type still merges into them.
func TestMergeMapsRetypedCheckDropsInheritedLevels(t *testing.T) {
	catalog := map[string]any{
		"type":     "metric",
		"name":     "memory",
		"op":       ">",
		"value":    "30%",
		"severity": "warning",
		"levels":   map[string]any{"error": map[string]any{"value": "50%"}},
	}
	retyped := mergeMaps(catalog, map[string]any{"type": "memory", "used_pct": map[string]any{"op": ">=", "value": 90}})
	if _, kept := retyped["levels"]; kept || retyped["severity"] != "warning" {
		t.Fatalf("retyped check = %v, want the inherited levels dropped and the rest kept", retyped)
	}
	own := map[string]any{"critical": map[string]any{"used_pct": map[string]any{"op": ">=", "value": 99}}}
	replaced := mergeMaps(catalog, map[string]any{"type": "memory", "levels": own})
	if levels := replaced["levels"].(map[string]any); len(levels) != 1 || levels["critical"] == nil {
		t.Fatalf("retyped levels = %v, want only the override's tiers", levels)
	}
	same := mergeMaps(catalog, map[string]any{"type": "metric", "levels": map[string]any{"critical": map[string]any{"value": "90%"}}})
	if levels := same["levels"].(map[string]any); levels["error"] == nil || levels["critical"] == nil {
		t.Fatalf("same-type levels = %v, want the override merged into the inherited tiers", levels)
	}
}
