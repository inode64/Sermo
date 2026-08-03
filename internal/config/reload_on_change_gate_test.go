package config

import (
	"os"
	"path/filepath"
	"testing"

	"sermo/internal/rules"
)

// writeHostConfig builds a host whose service `uses` a catalog entry, with an
// optional global defaults block and optional per-service overrides — the three
// layers an operator actually edits.
func writeHostConfig(t *testing.T, catalogService, defaultsBlock, serviceExtra string) *Config {
	t.Helper()
	root := repoRoot(t)
	dir := t.TempDir()
	enabled := filepath.Join(dir, "services")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "name: " + catalogService + "\nuses: " + catalogService + "\nmonitor: enabled\n" + serviceExtra
	if err := os.WriteFile(filepath.Join(enabled, catalogService+".yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	global := "engine: { backend: systemd }\n" +
		"paths:\n  services: [" + enabled + "]\n  runtime: /run/sermo\n" +
		"defaults:\n  policy: { cooldown: 5m }\n" + defaultsBlock
	path := filepath.Join(dir, "sermo.yml")
	if err := os.WriteFile(path, []byte(global), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, WithCatalogDirs(repoCatalogDir(root)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate issues = %v, want none", issues)
	}
	return cfg
}

func reloadRuleNames(t *testing.T, cfg *Config, service string) []string {
	t.Helper()
	resolved, errs := cfg.Resolve(service)
	if len(errs) != 0 {
		t.Fatalf("Resolve: %v", errs)
	}
	ruleMap, _ := resolved.Tree[rules.SectionRules].(map[string]any)
	var names []string
	for name := range ruleMap {
		if len(name) > 17 && name[:17] == "reload-on-change-" {
			names = append(names, name)
		}
	}
	return names
}

// systemd is the catalog service that ships reload_on_change paths.
const reloadCatalogService = "systemd"

func TestReloadOnChangeGeneratesRulesByDefault(t *testing.T) {
	cfg := writeHostConfig(t, reloadCatalogService, "", "")
	if got := reloadRuleNames(t, cfg, reloadCatalogService); len(got) == 0 {
		t.Fatal("want reload-on-change rules from the catalog, got none")
	}
}

// The gate is what lets one host turn config-driven reloads off. Validation
// accepting the key is not enough — the rules must actually disappear.
func TestDefaultsReloadOnChangeConfigFalseSuppressesRules(t *testing.T) {
	cfg := writeHostConfig(t, reloadCatalogService, "  reload_on_change: { config: false }\n", "")
	if got := reloadRuleNames(t, cfg, reloadCatalogService); len(got) != 0 {
		t.Fatalf("defaults config:false must suppress reload rules, got %v", got)
	}
}

// The per-service file on the host is the strongest layer, so it can switch the
// reloads back on for one service after a host-wide opt-out.
func TestServiceOverrideWinsOverDefaultsForReloadGate(t *testing.T) {
	cfg := writeHostConfig(t, reloadCatalogService,
		"  reload_on_change: { config: false }\n",
		"reload_on_change:\n  config: true\n")
	if got := reloadRuleNames(t, cfg, reloadCatalogService); len(got) == 0 {
		t.Fatal("the per-service override must win over defaults, got no rules")
	}
}

// The merge is deep: a host that only sets the gate must not wipe the catalog's
// paths, otherwise turning the permission back on would leave nothing to watch.
func TestDefaultsGateKeepsCatalogPaths(t *testing.T) {
	cfg := writeHostConfig(t, reloadCatalogService,
		"  reload_on_change: { config: true }\n", "")
	if got := reloadRuleNames(t, cfg, reloadCatalogService); len(got) == 0 {
		t.Fatal("setting only the gate must keep the catalog paths, got no rules")
	}
}

func TestReloadOnChangeRejectsBadGateAndUnknownKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block map[string]any
	}{
		{"non-boolean gate", map[string]any{"config": "yes-please"}},
		{"unknown sub-key", map[string]any{"apps": []any{"nginx"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := expandReloadOnChange(map[string]any{keyReloadOnChange: tc.block})
			if len(errs) == 0 {
				t.Fatal("want an error")
			}
		})
	}
}
