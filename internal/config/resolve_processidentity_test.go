package config

import (
	"testing"

	"sermo/internal/checks"
	"sermo/internal/rules"
	"sermo/internal/severity"
)

// A service with a strict selector gets the identity check, optional and
// graded warning so the service reads warning without losing availability,
// and an alert that never acts.
func TestProcessIdentityInjectsAWarningCheckAndAnAlert(t *testing.T) {
	tree := staleBinaryTree(map[string]any{
		"processes": map[string]any{"main": map[string]any{"exe": "/usr/bin/pm2", "user": "root"}},
	})
	if errs := expandProcessIdentity(tree); len(errs) > 0 {
		t.Fatalf("expandProcessIdentity: %v", errs)
	}
	entry := nested(t, tree, sectionChecks, ProcessIdentityCheckName)
	if entry[checks.CheckKeyType] != checks.CheckTypeProcessIdentity || entry[checks.CheckKeyOptional] != true ||
		entry[checks.CheckKeySeverity] != string(severity.Warning) {
		t.Fatalf("process-identity check = %v, want an optional warning check", entry)
	}
	rule := nested(t, tree, rules.SectionRules, processIdentityRuleName)
	if got := rule[rules.RuleFieldType]; got != string(rules.RuleAlert) {
		t.Fatalf("rule type = %v, want alert", got)
	}
	if got := ruleActionTypes(t, rule); len(got) != 1 || got[0] != "alert" {
		t.Fatalf("actions = %v, want alert only: a selector is corrected, not restarted", got)
	}
	if got := rule[rules.RuleFieldSeverity]; got != string(severity.Warning) {
		t.Fatalf("severity = %v, want warning", got)
	}
	if got := nested(t, rule, rules.RuleFieldFor)[rules.WindowKeyCycles]; got != processIdentityCycles {
		t.Fatalf("for cycles = %v, want %d: a starting launcher must not alert", got, processIdentityCycles)
	}
	if failed := nested(t, rule, rules.RuleFieldIf, rules.ConditionFailed); failed[rules.FieldCheck] != ProcessIdentityCheckName {
		t.Fatalf("condition = %v, want failed: %s", rule[rules.RuleFieldIf], ProcessIdentityCheckName)
	}
}

// Without a strict selector there is no identity an operation would refuse
// on, and a service with nothing resident has no process to judge.
func TestProcessIdentitySkipsServicesWithNothingToVerify(t *testing.T) {
	for name, tree := range map[string]map[string]any{
		"no selector":    staleBinaryTree(map[string]any{"processes": map[string]any{}}),
		"cmd only":       staleBinaryTree(map[string]any{"processes": map[string]any{"main": map[string]any{"cmd": "^tuned", "user": "root"}}}),
		"delegated only": staleBinaryTree(map[string]any{"processes": map[string]any{"main": map[string]any{"exe": "/usr/bin/qemu", "user": "qemu", "delegated": true}}}),
	} {
		if errs := expandProcessIdentity(tree); len(errs) > 0 {
			t.Fatalf("%s: %v", name, errs)
		}
		if section, _ := tree[sectionChecks].(map[string]any); section[ProcessIdentityCheckName] != nil {
			t.Fatalf("%s: injected %v", name, section[ProcessIdentityCheckName])
		}
	}
}
