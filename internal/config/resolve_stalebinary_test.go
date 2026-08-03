package config

import (
	"maps"
	"strings"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/rules"
)

func staleBinaryTree(extra map[string]any) map[string]any {
	tree := map[string]any{
		keyName:        "ovs-vswitchd",
		keyDisplayName: "Open vSwitch Server",
		"processes": map[string]any{
			"main": map[string]any{"exe": "/usr/bin/ovs-vswitchd", "user": "root"},
		},
	}
	maps.Copy(tree, extra)
	return tree
}

func staleBinaryGeneratedRule(t *testing.T, tree map[string]any) map[string]any {
	t.Helper()
	if errs := expandStaleBinary(tree); len(errs) > 0 {
		t.Fatalf("expandStaleBinary: %v", errs)
	}
	ruleMap, ok := tree[rules.SectionRules].(map[string]any)
	if !ok {
		t.Fatal("no rules section generated")
	}
	rule, ok := ruleMap[staleBinaryRuleName].(map[string]any)
	if !ok {
		t.Fatalf("rule %q not generated", staleBinaryRuleName)
	}
	return rule
}

// The flag absent means allowed, the convention restart_on_change already uses.
func TestStaleBinaryDefaultAllowsRestart(t *testing.T) {
	tree := staleBinaryTree(nil)
	rule := staleBinaryGeneratedRule(t, tree)

	if got := rule[rules.RuleFieldType]; got != string(rules.RuleRemediation) {
		t.Fatalf("want a remediation rule, got %v", got)
	}
	if got := ruleActionTypes(t, rule); len(got) != 2 || got[0] != "alert" || got[1] != "restart" {
		t.Fatalf("want alert then restart, got %v", got)
	}
	checksMap, _ := tree[sectionChecks].(map[string]any)
	entry, _ := checksMap[staleBinaryCheckName].(map[string]any)
	if entry[checks.CheckKeyType] != checks.CheckTypeStaleBinary {
		t.Fatalf("stale-binary check not injected: %v", checksMap)
	}
}

// This is the OVS case. The alert and its notification survive; only the
// restart action is dropped, which also downgrades the rule to `alert` because
// a remediation rule must carry an operation action.
func TestStaleBinaryFalseKeepsAlertDropsRestart(t *testing.T) {
	rule := staleBinaryGeneratedRule(t, staleBinaryTree(map[string]any{keyRestartOnStaleBinary: false}))

	if got := rule[rules.RuleFieldType]; got != string(rules.RuleAlert) {
		t.Fatalf("want an alert rule, got %v", got)
	}
	got := ruleActionTypes(t, rule)
	if len(got) != 1 || got[0] != "alert" {
		t.Fatalf("want alert only, got %v", got)
	}
}

// The veto is scoped to this trigger. A service that forbids the stale-binary
// restart must keep every rule it declared itself, including remediation that
// restarts on a real failure.
func TestStaleBinaryFalseLeavesOtherRemediationIntact(t *testing.T) {
	own := map[string]any{
		rules.RuleFieldType: string(rules.RuleRemediation),
		rules.RuleFieldIf:   map[string]any{rules.ConditionFailed: map[string]any{rules.FieldCheck: "port"}},
		rules.RuleFieldThen: map[string]any{rules.RuleFieldAction: "restart"},
	}
	tree := staleBinaryTree(map[string]any{
		keyRestartOnStaleBinary: false,
		rules.SectionRules:      map[string]any{"restart-when-down": own},
	})

	if errs := expandStaleBinary(tree); len(errs) > 0 {
		t.Fatalf("expandStaleBinary: %v", errs)
	}
	ruleMap, _ := tree[rules.SectionRules].(map[string]any)
	kept, ok := ruleMap["restart-when-down"].(map[string]any)
	if !ok {
		t.Fatal("the service's own remediation rule was dropped")
	}
	if kept[rules.RuleFieldType] != string(rules.RuleRemediation) {
		t.Fatalf("the service's own rule must stay remediation, got %v", kept[rules.RuleFieldType])
	}
	then, _ := kept[rules.RuleFieldThen].(map[string]any)
	if then[rules.RuleFieldAction] != "restart" {
		t.Fatalf("the service's own restart action must survive, got %v", then)
	}
}

// Nothing to discover means nothing can be stale; injecting a check there would
// report a permanent, meaningless OK.
func TestStaleBinarySkippedWithoutProcessSelectors(t *testing.T) {
	tree := map[string]any{keyName: "kernel-thing"}
	if errs := expandStaleBinary(tree); len(errs) > 0 {
		t.Fatalf("expandStaleBinary: %v", errs)
	}
	if _, ok := tree[rules.SectionRules]; ok {
		t.Fatal("no rule should be generated without process selectors")
	}
	if _, ok := tree[sectionChecks]; ok {
		t.Fatal("no check should be generated without process selectors")
	}
}

// A pidfile is a process selector too: those services can also run a replaced
// binary.
func TestStaleBinaryAppliesToPidfileServices(t *testing.T) {
	tree := map[string]any{keyName: "x", "pidfile": "/run/x.pid"}
	staleBinaryGeneratedRule(t, tree)
}

func TestStaleBinaryRejectsNonBoolFlag(t *testing.T) {
	errs := expandStaleBinary(staleBinaryTree(map[string]any{keyRestartOnStaleBinary: "yes"}))
	if len(errs) == 0 {
		t.Fatal("want an error for a non-boolean flag")
	}
}

// The generated names are reserved: silently overwriting a hand-written check
// or rule would hide the operator's own definition.
func TestStaleBinaryRefusesToOverwrite(t *testing.T) {
	for _, tc := range []struct {
		name string
		tree map[string]any
	}{
		{"check", staleBinaryTree(map[string]any{sectionChecks: map[string]any{staleBinaryCheckName: map[string]any{}}})},
		{"rule", staleBinaryTree(map[string]any{rules.SectionRules: map[string]any{staleBinaryRuleName: map[string]any{}}})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if errs := expandStaleBinary(tc.tree); len(errs) == 0 {
				t.Fatal("want an error rather than a silent overwrite")
			}
		})
	}
}

// The message must name the service and keep the runtime placeholder for the
// worker to fill.
func TestStaleBinaryMessageNamesServiceAndKeepsPlaceholder(t *testing.T) {
	got := staleBinaryAlertMessage(staleBinaryTree(nil))
	if !strings.HasPrefix(got, "Open vSwitch Server") {
		t.Fatalf("message must start with the display name, got %q", got)
	}
	if !strings.Contains(got, "${check.value}") {
		t.Fatalf("message must keep ${check.value}, got %q", got)
	}
}
