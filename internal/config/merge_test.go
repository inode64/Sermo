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
