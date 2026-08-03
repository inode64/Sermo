package config

import (
	"os"
	"path/filepath"
	"testing"

	"sermo/internal/rules"
)

// resolveCatalogService resolves one real catalog service the way the daemon
// would, so these assertions are about the shipped catalog rather than a
// hand-built tree.
func resolveCatalogService(t *testing.T, catalogService, backend string) Resolved {
	t.Helper()
	root := repoRoot(t)
	dir := t.TempDir()
	enabled := filepath.Join(dir, "services")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "name: " + catalogService + "\nuses: " + catalogService + "\nmonitor: enabled\n"
	if err := os.WriteFile(filepath.Join(enabled, catalogService+".yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(writeServicesGlobal(t, dir, enabled, backend), WithCatalogDirs(repoCatalogDir(root)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate issues = %v, want none", issues)
	}
	resolved, errs := cfg.Resolve(catalogService)
	if len(errs) != 0 {
		t.Fatalf("Resolve: %v", errs)
	}
	return resolved
}

func staleBinaryRuleOf(t *testing.T, resolved Resolved) map[string]any {
	t.Helper()
	return nested(t, resolved.Tree, rules.SectionRules, staleBinaryRuleName)
}

func ruleActionTypes(t *testing.T, rule map[string]any) []string {
	t.Helper()
	then, _ := rule[rules.RuleFieldThen].(map[string]any)
	raw, _ := then[rules.RuleFieldActions].([]any)
	out := make([]string, 0, len(raw))
	for _, a := range raw {
		m, _ := a.(map[string]any)
		out = append(out, m[rules.RuleFieldType].(string))
	}
	return out
}

// The OVS services must never restart themselves over a replaced binary:
// restarting the dataplane cuts the host off. They must still alert, so the
// operator can schedule the restart by hand.
func TestCatalogOVSNeverAutoRestartsOnStaleBinary(t *testing.T) {
	for _, service := range []string{"ovs-vswitchd", "ovsdb-server"} {
		t.Run(service, func(t *testing.T) {
			rule := staleBinaryRuleOf(t, resolveCatalogService(t, service, "openrc"))
			if got := rule[rules.RuleFieldType]; got != string(rules.RuleAlert) {
				t.Fatalf("want an alert rule, got %v", got)
			}
			for _, action := range ruleActionTypes(t, rule) {
				if action == string(rules.ActionRestart) {
					t.Fatal("OVS must not carry an automatic restart for a replaced binary")
				}
			}
		})
	}
}

// ovsdb-client declares no process selectors, so there is nothing to find
// stale and no rule is generated. Its restart_on_stale_binary: false is
// deliberate anyway: it pre-arms the veto for the day selectors are added.
func TestCatalogOVSClientHasNothingToCheck(t *testing.T) {
	resolved := resolveCatalogService(t, "ovsdb-client", "openrc")
	ruleMap, _ := resolved.Tree[rules.SectionRules].(map[string]any)
	if _, generated := ruleMap[staleBinaryRuleName]; generated {
		t.Fatal("ovsdb-client declares no processes; a stale-binary rule must not appear")
	}
}

// Both doc files promise `defaults.restart_on_stale_binary: false` opts a whole
// host out at once. Accepting the key in validation is not enough — this asserts
// it actually reaches the generated rule.
func TestDefaultsRestartOnStaleBinaryOptsOutWholeHost(t *testing.T) {
	root := repoRoot(t)
	dir := t.TempDir()
	enabled := filepath.Join(dir, "services")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(enabled, "nginx.yml"),
		[]byte("name: nginx\nuses: nginx\nmonitor: enabled\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(dir, "sermo.yml")
	body := "engine: { backend: systemd }\n" +
		"paths:\n  services: [" + enabled + "]\n  runtime: /run/sermo\n" +
		"defaults:\n  policy: { cooldown: 5m }\n  restart_on_stale_binary: false\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global, WithCatalogDirs(repoCatalogDir(root)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate issues = %v, want none", issues)
	}
	resolved, errs := cfg.Resolve("nginx")
	if len(errs) != 0 {
		t.Fatalf("Resolve: %v", errs)
	}

	rule := staleBinaryRuleOf(t, resolved)
	if got := rule[rules.RuleFieldType]; got != string(rules.RuleAlert) {
		t.Fatalf("defaults must opt the host out, got rule type %v", got)
	}
	for _, action := range ruleActionTypes(t, rule) {
		if action == string(rules.ActionRestart) {
			t.Fatal("defaults: false must drop the restart action")
		}
	}
}

// Everything else keeps the restart, which is what was asked for: the veto is
// OVS-only.
func TestCatalogOtherServicesRestartOnStaleBinary(t *testing.T) {
	for _, service := range []string{"nginx", "nfs", "mariadb"} {
		t.Run(service, func(t *testing.T) {
			rule := staleBinaryRuleOf(t, resolveCatalogService(t, service, "systemd"))
			if got := rule[rules.RuleFieldType]; got != string(rules.RuleRemediation) {
				t.Fatalf("want a remediation rule, got %v", got)
			}
			actions := ruleActionTypes(t, rule)
			if len(actions) != 2 || actions[0] != string(rules.ActionAlert) || actions[1] != string(rules.ActionRestart) {
				t.Fatalf("want alert then restart, got %v", actions)
			}
		})
	}
}

// allow_dependencies is inheritable from global `defaults:` like dry_run. It
// needs to be in two parallel registries (perServiceDefaults and
// validDefaultsKeys); this asserts the value survives resolution rather than
// just passing validation.
func TestDefaultsAllowDependenciesReachesResolvedService(t *testing.T) {
	for _, tc := range []struct{ defaults, want bool }{{false, false}, {true, true}} {
		root := repoRoot(t)
		dir := t.TempDir()
		enabled := filepath.Join(dir, "services")
		if err := os.MkdirAll(enabled, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(enabled, "nginx.yml"),
			[]byte("name: nginx\nuses: nginx\nmonitor: enabled\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		body := "engine: { backend: systemd }\n" +
			"paths:\n  services: [" + enabled + "]\n  runtime: /run/sermo\n" +
			"defaults:\n  policy: { cooldown: 5m }\n"
		if tc.defaults {
			body += "  allow_dependencies: true\n"
		}
		global := filepath.Join(dir, "sermo.yml")
		if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(global, WithCatalogDirs(repoCatalogDir(root)))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if issues := Validate(cfg); len(issues) != 0 {
			t.Fatalf("Validate issues = %v, want none", issues)
		}
		resolved, errs := cfg.Resolve("nginx")
		if len(errs) != 0 {
			t.Fatalf("Resolve: %v", errs)
		}
		if got := AllowDependencies(resolved.Tree); got != tc.want {
			t.Fatalf("defaults allow_dependencies=%v -> resolved %v, want %v", tc.defaults, got, tc.want)
		}
	}
}
