package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"sermo/internal/cfgval"
)

func cfgvalString(v any) string { return cfgval.AsString(v) }

type reloadSignalService struct {
	name   string
	signal string
}

// TestRealCatalogReloadServicesResolve loads the actual repo catalog and resolves
// each catalog service that ships a native `reload:` block, asserting the block survives
// resolution and validates. It guards the catalog YAML against typos in the
// reload feature and confirms the block reaches the resolved service tree.
func TestRealCatalogReloadServicesResolve(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	writeGlobal := func(dir, enabled, backend string) string {
		t.Helper()
		global := filepath.Join(dir, "sermo.yml")
		body := "engine: { backend: " + backend + " }\n" +
			"paths:\n  catalog: [" + catalogDir + "]\n  services: [" + enabled + "]\n  runtime: /run/sermo\n" +
			"defaults:\n  policy: { cooldown: 5m }\n"
		if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return global
	}

	for _, backend := range []string{"systemd", "openrc"} {
		t.Run(backend, func(t *testing.T) {
			probeDir := t.TempDir()
			probeEnabled := filepath.Join(probeDir, "services")
			if err := os.MkdirAll(probeEnabled, 0o755); err != nil {
				t.Fatal(err)
			}
			probe, err := Load(writeGlobal(probeDir, probeEnabled, backend))
			if err != nil {
				t.Fatalf("Load (probe): %v", err)
			}
			services := catalogReloadSignalServices(probe)
			if len(services) == 0 {
				t.Fatal("no catalog services with reload.signal found")
			}

			dir := t.TempDir()
			enabled := filepath.Join(dir, "services")
			if err := os.MkdirAll(enabled, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, d := range services {
				svc := "name: " + d.name + "-main\nuses: " + d.name + "\n"
				if err := os.WriteFile(filepath.Join(enabled, d.name+".yml"), []byte(svc), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			cfg, err := Load(writeGlobal(dir, enabled, backend))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if issues := Validate(cfg); len(issues) != 0 {
				t.Fatalf("Validate issues = %v, want none", issues)
			}
			for _, d := range services {
				resolved, errs := cfg.Resolve(d.name + "-main")
				if len(errs) != 0 {
					t.Errorf("%s: resolve errors = %v", d.name, errs)
					continue
				}
				r, ok := resolved.Tree["reload"].(map[string]any)
				if !ok {
					t.Errorf("%s: resolved tree has no reload block", d.name)
					continue
				}
				// The catalog leaves `when` unset — absent means the default auto mode,
				// so any explicit value here would be a redundancy regression.
				if cfgvalString(r["signal"]) != d.signal {
					t.Errorf("%s: reload = %v, want signal %s", d.name, r, d.signal)
				}
				if _, ok := r["when"]; ok {
					t.Errorf("%s: reload restates when (%v); absent means auto", d.name, r["when"])
				}
			}
		})
	}
}

func catalogReloadSignalServices(cfg *Config) []reloadSignalService {
	var out []reloadSignalService
	for _, name := range cfg.CatalogServiceNames {
		if strings.Contains(name, "%") {
			continue
		}
		doc := cfg.CatalogServices[name]
		if doc == nil {
			continue
		}
		reload, ok := doc.Body["reload"].(map[string]any)
		if !ok {
			continue
		}
		if signal := cfgvalString(reload["signal"]); signal != "" {
			out = append(out, reloadSignalService{name: name, signal: signal})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}
