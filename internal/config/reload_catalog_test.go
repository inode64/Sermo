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
	for _, backend := range []string{"systemd", "openrc"} {
		t.Run(backend, func(t *testing.T) {
			validateCatalogReloadServices(t, catalogDir, backend)
		})
	}
}

func validateCatalogReloadServices(t *testing.T, catalogDir, backend string) {
	t.Helper()
	services := loadCatalogReloadSignalServices(t, catalogDir, backend)
	dir := t.TempDir()
	enabled := filepath.Join(dir, "services")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	writeReloadTestServices(t, enabled, services)
	cfg, err := Load(writeServicesGlobal(t, dir, enabled, backend), WithCatalogDirs(catalogDir))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate issues = %v, want none", issues)
	}
	assertCatalogReloadServices(t, cfg, services)
}

func loadCatalogReloadSignalServices(t *testing.T, catalogDir, backend string) []reloadSignalService {
	t.Helper()
	dir := t.TempDir()
	enabled := filepath.Join(dir, "services")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(writeServicesGlobal(t, dir, enabled, backend), WithCatalogDirs(catalogDir))
	if err != nil {
		t.Fatalf("Load (probe): %v", err)
	}
	services := catalogReloadSignalServices(cfg)
	if len(services) == 0 {
		t.Fatal("no catalog services with reload.signal found")
	}
	return services
}

func writeReloadTestServices(t *testing.T, enabled string, services []reloadSignalService) {
	t.Helper()
	for _, service := range services {
		body := "name: " + service.name + "-main\nuses: " + service.name + "\n"
		if err := os.WriteFile(filepath.Join(enabled, service.name+".yml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func assertCatalogReloadServices(t *testing.T, cfg *Config, services []reloadSignalService) {
	t.Helper()
	for _, service := range services {
		resolved, errs := cfg.Resolve(service.name + "-main")
		if len(errs) != 0 {
			t.Errorf("%s: resolve errors = %v", service.name, errs)
			continue
		}
		reload, ok := resolved.Tree["reload"].(map[string]any)
		if !ok {
			t.Errorf("%s: resolved tree has no reload block", service.name)
			continue
		}
		if cfgvalString(reload["signal"]) != service.signal {
			t.Errorf("%s: reload = %v, want signal %s", service.name, reload, service.signal)
		}
		if _, ok := reload["when"]; ok {
			t.Errorf("%s: reload restates when (%v); absent means auto", service.name, reload["when"])
		}
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
