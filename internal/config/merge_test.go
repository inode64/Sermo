package config

import (
	"maps"
	"slices"
	"testing"

	"sermo/internal/process"
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

// TestDisabledProcessSelectorIsDropped pins the merge rule for `processes`:
// validation refused `enabled` there although the section is mergeable and the
// flag documented for it. A selector has no disabled state, so the entry goes, and
// with the last one gone the service falls back on the init backend's identity.
func TestDisabledProcessSelectorIsDropped(t *testing.T) {
	catalog := `
name: base
service: base
processes:
  main: { exe: /usr/bin/base, user: root }
  worker: { exe: /usr/bin/worker, user: root }
`
	cases := []struct {
		name, override string
		want           []string
		wantMode       ServiceProcessMode
	}{
		{"one disabled", "processes:\n  worker: { enabled: false }\n", []string{"main"}, ServiceProcessResident},
		{"all disabled", "processes:\n  main: { enabled: false }\n  worker: { enabled: false }\n", nil, ServiceProcessInit},
		{"enabled stays", "processes:\n  worker: { enabled: true }\n", []string{"main", "worker"}, ServiceProcessResident},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadConfig(t, writeConfig(t, map[string]string{
				"sermo.yml":                 baseGlobal,
				"catalog/services/base.yml": catalog,
				"services/svc.yml":          "name: svc\nuses: base\n" + tc.override,
			}))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			mustNotHave(t, Validate(cfg), "processes.")
			resolved, errs := cfg.Resolve("svc")
			if len(errs) != 0 {
				t.Fatalf("Resolve() errors = %v", errs)
			}
			got := slices.Sorted(maps.Keys(SectionMap(resolved.Tree, process.SectionProcesses)))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("processes = %v, want %v", got, tc.want)
			}
			if mode := ResolveServiceLifecycle(resolved.Tree, backendSystemd).ProcessMode; mode != tc.wantMode {
				t.Fatalf("ProcessMode = %q, want %q", mode, tc.wantMode)
			}
		})
	}
}

func TestCloneReenablesDisabledProcessSelector(t *testing.T) {
	for _, tc := range []struct{ name, source, clone string }{
		{"local", "services/base.yml", "base"},
		{"catalog", "catalog/services/base.yml", "base"},
		{"local chain", "services/base.yml", "middle"},
		{"catalog chain", "catalog/services/base.yml", "middle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{
				"sermo.yml":           baseGlobal,
				"services/base.yml":   "name: base\nuses: base\n",
				"services/middle.yml": "name: middle\nclone: base\nservice: middle\n",
				"services/child.yml":  "name: child\nclone: " + tc.clone + "\nservice: child\nprocesses:\n  main: { enabled: true }\n",
			}
			files[tc.source] = `
name: base
service: base
processes:
  main: { exe: /usr/bin/base, user: root, cmd: "base --serve", enabled: false }
  worker: { exe: /usr/bin/worker, user: nobody, delegated: true, enabled: false }
`
			cfg, err := loadConfig(t, writeConfig(t, files))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			mustNotHave(t, Validate(cfg), "processes.")
			child, errs := cfg.Resolve("child")
			if len(errs) != 0 {
				t.Fatalf("Resolve(child) errors = %v", errs)
			}
			selectors := SectionMap(child.Tree, process.SectionProcesses)
			main := SectionMap(selectors, "main")
			if len(selectors) != 1 || main[process.SelectorKeyExe] != "/usr/bin/base" || main[process.SelectorKeyUser] != "root" || main[process.SelectorKeyCmd] != "base --serve" {
				t.Fatalf("reactivated selector lost its inherited identity: %v", selectors)
			}
			for _, name := range []string{"base", "middle"} {
				resolved, resolveErrs := cfg.Resolve(name)
				if len(resolveErrs) != 0 {
					t.Fatalf("Resolve(%s) errors = %v", name, resolveErrs)
				}
				if mode := ResolveServiceLifecycle(resolved.Tree, backendSystemd).ProcessMode; mode != ServiceProcessInit {
					t.Fatalf("%s retained disabled selectors: %v", name, resolved.Tree)
				}
			}
			if tc.source == "catalog/services/base.yml" {
				catalog, catalogErrs := cfg.ResolveCatalog(CategoryService, "base")
				if len(catalogErrs) != 0 || ResolveServiceLifecycle(catalog.Tree, backendSystemd).ProcessMode != ServiceProcessInit {
					t.Fatalf("catalog retained disabled selectors: %v, errors = %v", catalog.Tree, catalogErrs)
				}
			}
		})
	}
}
