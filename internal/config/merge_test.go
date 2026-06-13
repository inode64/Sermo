package config

import "testing"

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
