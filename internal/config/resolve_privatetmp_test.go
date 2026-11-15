package config

import (
	"testing"

	"sermo/internal/checks"
	"sermo/internal/rules"
	"sermo/internal/severity"
)

// Every service whose processes discovery attributes gets the private-tmp
// sensor and a rule that alerts and then restarts: a unit that lost its private
// /tmp cannot reload or stop cleanly until it gets a new namespace.
func TestPrivateTmpInjectsSensorAndRestartRule(t *testing.T) {
	tree := staleBinaryTree(nil)
	if errs := expandPrivateTmp(tree); len(errs) > 0 {
		t.Fatalf("expandPrivateTmp: %v", errs)
	}
	entry := nested(t, tree, sectionChecks, PrivateTmpCheckName)
	if entry[checks.CheckKeyType] != checks.CheckTypePrivateTmp || entry[checks.CheckKeyReports] != checks.ReportsState ||
		entry[EntryKeyInterval] != privateTmpCheckInterval {
		t.Fatalf("private-tmp check = %v, want a state sensor every %s", entry, privateTmpCheckInterval)
	}
	rule := nested(t, tree, rules.SectionRules, privateTmpRuleName)
	if got := rule[rules.RuleFieldType]; got != string(rules.RuleRemediation) {
		t.Fatalf("rule type = %v, want remediation", got)
	}
	if got := ruleActionTypes(t, rule); len(got) != 2 || got[0] != "alert" || got[1] != "restart" {
		t.Fatalf("actions = %v, want alert then restart", got)
	}
	if got := rule[rules.RuleFieldSeverity]; got != string(severity.Warning) {
		t.Fatalf("severity = %v, want warning: the daemon still serves", got)
	}
	if failed := nested(t, rule, rules.RuleFieldIf, rules.ConditionFailed); failed[rules.FieldCheck] != PrivateTmpCheckName {
		t.Fatalf("condition = %v, want failed: %s", rule[rules.RuleFieldIf], PrivateTmpCheckName)
	}
}

// A delegated selector's processes belong to another manager, so the rule only
// alerts; a service with nothing resident gets neither entry.
func TestPrivateTmpDelegatedAlertsAndNonResidentSkips(t *testing.T) {
	delegated := staleBinaryTree(map[string]any{
		SectionControl: map[string]any{"type": "libvirt", "domain": "a"},
		"processes":    map[string]any{"main": map[string]any{"cmd": "^qemu -name guest=a,", "delegated": true}},
	})
	if errs := expandPrivateTmp(delegated); len(errs) > 0 {
		t.Fatalf("expandPrivateTmp: %v", errs)
	}
	if got := ruleActionTypes(t, nested(t, delegated, rules.SectionRules, privateTmpRuleName)); len(got) != 1 || got[0] != "alert" {
		t.Fatalf("delegated actions = %v, want alert only", got)
	}

	none := staleBinaryTree(map[string]any{"processes": map[string]any{}})
	if errs := expandPrivateTmp(none); len(errs) > 0 {
		t.Fatalf("expandPrivateTmp: %v", errs)
	}
	if _, ok := none[sectionChecks]; ok {
		t.Fatalf("processes: {} got checks %v", none[sectionChecks])
	}
}

// The injected names are reserved: an operator's entry is never overwritten.
func TestPrivateTmpRefusesToShadowAnOperatorEntry(t *testing.T) {
	tree := staleBinaryTree(map[string]any{sectionChecks: map[string]any{PrivateTmpCheckName: map[string]any{"type": "tcp"}}})
	if errs := expandPrivateTmp(tree); len(errs) != 1 {
		t.Fatalf("errs = %v, want one refusal", errs)
	}
}
