package config

import (
	"os"
	"path/filepath"
	"testing"

	"sermo/internal/cfgval"
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

// TestCatalogSNMPDProtocolProbeGatedOnLocalCommunity pins the gate that kept
// agents amber forever: snmpd answers a v1/v2c query only from the sources its
// community grants, so the local probe runs only when the first rocommunity
// line grants localhost. Asserted on the raw document because a resolved tree
// prunes the gate on any host without /etc/snmp/snmpd.conf, this one included.
func TestCatalogSNMPDProtocolProbeGatedOnLocalCommunity(t *testing.T) {
	root := repoRoot(t)
	snmpd := catalogDocByName(t, root, "services", "snmpd")
	port := nested(t, snmpd, "watches", "port")
	if got := cfgval.String(nested(t, port, "check")["type"]); got != "snmp" {
		t.Fatalf("snmpd port check type = %q, want snmp", got)
	}
	gate := nested(t, port, "enable_if")
	if got := cfgval.String(gate["file"]); got != "/etc/snmp/snmpd.conf" {
		t.Fatalf("snmpd probe gate file = %q, want /etc/snmp/snmpd.conf", got)
	}
	if got := cfgval.String(gate["key"]); got != "rocommunity" {
		t.Fatalf("snmpd probe gate key = %q, want rocommunity", got)
	}
	tests := []struct {
		value string
		want  bool
	}{
		{"public", true},
		{"public default", true},
		{"public localhost", true},
		{"public 127.0.0.1/8", true},
		{"public -V systemonly", true},
		{"public default -V systemonly", true},
		{"public 172.31.25.0/24", false},
		{"public 10.0.0.0/8 -V systemonly", false},
	}
	for _, tt := range tests {
		if got := enableIfPredicateMatches(gate, tt.value); got != tt.want {
			t.Errorf("rocommunity %q keeps the local probe = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func TestCatalogFcronBlocksStopAndRestartWithActiveJobs(t *testing.T) {
	const guardName = "block-stop-restart-with-active-jobs"
	resolved := resolveCatalogService(t, "fcron", "systemd")
	check := nested(t, resolved.Tree, "checks", guardName)
	if got := cfgval.String(check["type"]); got != "process_count" {
		t.Fatalf("fcron active-jobs check type = %q, want process_count", got)
	}
	if got := cfgval.String(check["reports"]); got != "state" {
		t.Fatalf("fcron active-jobs reports = %q, want state", got)
	}
	count := nested(t, check, "count")
	if got := cfgval.String(count["op"]); got != ">" {
		t.Fatalf("fcron active-jobs count op = %q, want >", got)
	}
	if got := cfgval.String(count["value"]); got != "1" {
		t.Fatalf("fcron active-jobs count value = %q, want 1", got)
	}

	guard := nested(t, resolved.Tree, rules.SectionRules, guardName)
	if got := guard[rules.RuleFieldType]; got != string(rules.RuleGuard) {
		t.Fatalf("fcron active-jobs rule type = %v, want guard", got)
	}
	blocks := cfgval.StringList(guard[rules.RuleFieldBlocks])
	if len(blocks) != 2 || blocks[0] != string(rules.ActionRestart) || blocks[1] != string(rules.ActionStop) {
		t.Fatalf("fcron active-jobs blocks = %v, want [restart stop]", blocks)
	}
	condition := nested(t, guard, rules.RuleFieldIf, string(rules.ConditionActive))
	if got := cfgval.String(condition[rules.FieldCheck]); got != guardName {
		t.Fatalf("fcron active-jobs condition check = %q, want %q", got, guardName)
	}
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
	for _, service := range []string{"nginx", "rpc-mountd", "mariadb"} {
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

func TestCatalogNFSAllowsInitDependencies(t *testing.T) {
	t.Parallel()

	systemd := resolveCatalogService(t, "nfs", backendSystemd)
	processes, ok := systemd.Tree[SectionProcesses].(map[string]any)
	if !ok || len(processes) != 0 {
		t.Fatalf("nfs processes = %v, want an explicit no-resident-process map", systemd.Tree[SectionProcesses])
	}
	if !AllowDependencies(systemd.Tree) {
		t.Fatal("nfs systemd must allow init dependencies")
	}
	openrc := resolveCatalogService(t, "nfs", backendOpenRC)
	if !AllowDependencies(openrc.Tree) {
		t.Fatal("nfs openrc must preserve the resolved allow_dependencies policy")
	}
}

// allow_dependencies is inheritable from global `defaults:` like dry_run. It
// needs to be in two parallel registries (perServiceDefaults and
// validDefaultsKeys); this asserts the value survives resolution rather than
// just passing validation.
func TestDefaultsAllowDependenciesReachesResolvedService(t *testing.T) {
	for _, tc := range []struct {
		name     string
		defaults string
		want     bool
	}{
		{"absent", "", false},
		{"set", "  allow_dependencies: true\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := writeHostConfig(t, "nginx", tc.defaults, "")
			resolved, errs := cfg.Resolve("nginx")
			if len(errs) != 0 {
				t.Fatalf("Resolve: %v", errs)
			}
			if got := AllowDependencies(resolved.Tree); got != tc.want {
				t.Fatalf("defaults %q -> resolved %v, want %v", tc.defaults, got, tc.want)
			}
		})
	}
}

// The generated rule must fire when the check FAILS, not when it passes.
// eval.go reads `active:` as "the check is OK", and the stale-binary check is
// OK when nothing is stale — so `active:` inverted the rule: it restarted
// healthy services and stayed silent once a binary was actually replaced. Every
// service that declares processes carries this rule, and dry_run was the only
// thing keeping the fleet from acting on it.
func TestStaleBinaryRuleFiresOnCheckFailureNotOnHealth(t *testing.T) {
	rule := staleBinaryRule(true, "irrelevant")
	cond, ok := rule[rules.RuleFieldIf].(map[string]any)
	if !ok {
		t.Fatalf("rule has no condition mapping: %#v", rule)
	}
	if _, wrong := cond[rules.ConditionActive]; wrong {
		t.Error("condition uses active:, which is true while the service is healthy")
	}
	target, ok := cond[rules.ConditionFailed].(map[string]any)
	if !ok {
		t.Fatalf("condition is not failed:, got %#v", cond)
	}
	if got := target[rules.FieldCheck]; got != staleBinaryCheckName {
		t.Errorf("failed: check = %v, want %q", got, staleBinaryCheckName)
	}
}
