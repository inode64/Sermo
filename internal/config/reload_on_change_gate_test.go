package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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
		if strings.HasPrefix(name, reloadOnChangeRulePrefix) {
			names = append(names, name)
		}
	}
	return names
}

const (
	// systemd is the catalog service that ships reload_on_change paths.
	reloadCatalogService = "systemd"
	// reloadOnChangeRulePrefix is the name expandReloadOnChange generates.
	reloadOnChangeRulePrefix = "reload-on-change-"
)

func TestReloadOnChangeGeneratesRulesByDefault(t *testing.T) {
	cfg := writeHostConfig(t, reloadCatalogService, "", "")
	if got := reloadRuleNames(t, cfg, reloadCatalogService); len(got) == 0 {
		t.Fatal("want reload-on-change rules from the catalog, got none")
	}
}

// The gate is what lets one host turn config-driven reloads off. Validation
// accepting the key is not enough — the rules must actually disappear. This is
// also what pins the deep merge: under a replacing merge the catalog's block
// would overwrite the host's gate and the rules would survive.
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

// Every inheritable key must be accepted under global `defaults:` and merged
// into the service. The two lists used to be maintained by hand and a key once
// shipped documented-but-rejected; validDefaultsKeys is now derived, and this
// pins that so a future refactor cannot silently split them again.
//
// It covers key N+1 for free, which a per-key propagation test cannot.
func TestEveryPerServiceDefaultIsAcceptedUnderDefaults(t *testing.T) {
	for _, key := range perServiceDefaults {
		if _, ok := validDefaultsKeys[key]; !ok {
			t.Errorf("%q is merged into services but rejected under defaults:", key)
		}
	}
	// The reverse holds except for the one deliberately defaults-only key.
	for key := range validDefaultsKeys {
		if key == sectionVariables {
			continue
		}
		if !slices.Contains(perServiceDefaults, key) {
			t.Errorf("%q is accepted under defaults: but never merged into a service", key)
		}
	}
}

// A closed gate suppresses the rules, not the checking of what would have
// generated them: a malformed paths list must still fail loudly rather than lie
// dormant until someone turns the gate back on.
func TestReloadOnChangeStillReportsBadPathsWhenGateClosed(t *testing.T) {
	tree := map[string]any{
		keyReloadOnChange: map[string]any{
			keyRestartConfig: false,
			keyPaths:         []any{123},
		},
	}
	errs := expandReloadOnChange(tree)
	if len(errs) != 1 || !strings.Contains(errs[0], reloadOnChangePathPaths) {
		t.Fatalf("want the malformed-paths error even with the gate closed, got %v", errs)
	}
	if _, generated := tree[rules.SectionRules]; generated {
		t.Fatal("a closed gate must still generate no rules")
	}
}
