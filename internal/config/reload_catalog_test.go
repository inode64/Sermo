package config

import (
	"os"
	"path/filepath"
	"testing"

	"sermo/internal/cfgval"
)

func cfgvalString(v any) string { return cfgval.AsString(v) }

// TestRealCatalogReloadDaemonsResolve loads the actual repo catalog and resolves
// each daemon that ships a native `reload:` block, asserting the block survives
// resolution and validates. It guards the catalog YAML against typos in the
// reload feature and confirms the block reaches the resolved service tree.
func TestRealCatalogReloadDaemonsResolve(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	catalogDir := filepath.Join(root, "catalog")
	if _, err := os.Stat(catalogDir); err != nil {
		t.Skipf("catalog dir not found: %v", err)
	}

	dir := t.TempDir()
	enabled := filepath.Join(dir, "enabled")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	daemons := []string{"ssh", "snmpd", "proftpd", "prometheus", "loki"}
	for _, d := range daemons {
		svc := "kind: service\nname: " + d + "-main\nuses: " + d + "\n"
		if err := os.WriteFile(filepath.Join(enabled, d+".yml"), []byte(svc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	global := filepath.Join(dir, "sermo.yml")
	body := "engine: { backend: systemd }\n" +
		"paths:\n  catalog: [" + catalogDir + "]\n  includes: [" + enabled + "]\n  runtime: /run/sermo\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate issues = %v, want none", issues)
	}
	for _, d := range daemons {
		resolved, errs := cfg.Resolve(d + "-main")
		if len(errs) != 0 {
			t.Errorf("%s: resolve errors = %v", d, errs)
			continue
		}
		r, ok := resolved.Tree["reload"].(map[string]any)
		if !ok {
			t.Errorf("%s: resolved tree has no reload block", d)
			continue
		}
		// The catalog leaves `when` unset — absent means the default auto mode,
		// so any explicit value here would be a redundancy regression.
		if cfgvalString(r["signal"]) != "HUP" {
			t.Errorf("%s: reload = %v, want signal HUP", d, r)
		}
		if _, ok := r["when"]; ok {
			t.Errorf("%s: reload restates when (%v); absent means auto", d, r["when"])
		}
	}
}
