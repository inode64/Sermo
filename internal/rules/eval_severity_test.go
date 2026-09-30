package rules

import (
	"testing"

	"sermo/internal/checks"
	"sermo/internal/severity"
)

func TestRuleSeverity(t *testing.T) {
	cache := map[string]checks.Result{
		// A condition check whose threshold holds, graded by its levels.
		"memory": {Check: "memory", Condition: true, OK: true, Severity: severity.Error},
		"cpu":    {Check: "cpu", Condition: true, OK: true, Severity: severity.Warning},
		// A health check that fails, undeclared: an error.
		"tcp": {Check: "tcp"},
		// A healthy health check.
		"http": {Check: "http", OK: true, Severity: severity.Critical},
		// A condition check whose threshold does not hold.
		"quiet": {Check: "quiet", Condition: true, Severity: severity.Critical},
	}
	ref := func(op, name string) map[string]any {
		return map[string]any{op: map[string]any{FieldCheck: name}}
	}
	tests := []struct {
		name string
		rule Rule
		want severity.Level
	}{
		{"declared wins", Rule{Severity: severity.Info, If: ref(ConditionActive, "memory")}, severity.Info},
		{"graded active leaf", Rule{If: ref(ConditionActive, "memory")}, severity.Error},
		{"gravest of an or", Rule{If: map[string]any{ConditionOr: []any{ref(ConditionActive, "cpu"), ref(ConditionActive, "memory")}}}, severity.Error},
		{"failing health check", Rule{If: ref(ConditionFailed, "tcp")}, severity.Error},
		{"healthy leaves carry no grade", Rule{If: map[string]any{ConditionOr: []any{ref(ConditionActive, "http"), ref(ConditionActive, "quiet"), ref(ConditionActive, "cpu")}}}, severity.Warning},
		{"not subtrees are skipped", Rule{If: map[string]any{ConditionAnd: []any{ref(ConditionActive, "cpu"), map[string]any{ConditionNot: ref(ConditionFailed, "tcp")}}}}, severity.Warning},
		// The or holds only through cpu: the and branch is false, so its
		// failing memory check must not grade the rule.
		{"a false and branch carries no grade", Rule{If: map[string]any{ConditionOr: []any{
			map[string]any{ConditionAnd: []any{ref(ConditionActive, "memory"), ref(ConditionActive, "quiet")}},
			ref(ConditionActive, "cpu"),
		}}}, severity.Warning},
		{"a holding and branch grades", Rule{If: map[string]any{ConditionAnd: []any{ref(ConditionActive, "memory"), ref(ConditionActive, "cpu")}}}, severity.Error},
		{"no graded leaf is an error", Rule{If: map[string]any{ConditionMetric: map[string]any{"name": "memory"}}}, severity.Error},
		{"unknown reference is an error", Rule{If: ref(ConditionActive, "ghost")}, severity.Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &Evaluator{Cache: cache}
			if got := e.RuleSeverity(tt.rule); got != tt.want {
				t.Fatalf("RuleSeverity = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseRuleSeverity(t *testing.T) {
	tree := map[string]any{SectionRules: map[string]any{
		"alert-high": map[string]any{
			RuleFieldType: string(RuleAlert), RuleFieldSeverity: "critical",
			RuleFieldIf:   map[string]any{ConditionActive: map[string]any{FieldCheck: "c"}},
			RuleFieldThen: map[string]any{RuleFieldAction: string(ActionAlert), RuleFieldMessage: "high"},
		},
		"alert-typo": map[string]any{
			RuleFieldType: string(RuleAlert), RuleFieldSeverity: "urgent",
			RuleFieldIf:   map[string]any{ConditionActive: map[string]any{FieldCheck: "c"}},
			RuleFieldThen: map[string]any{RuleFieldAction: string(ActionAlert), RuleFieldMessage: "high"},
		},
		"guard": map[string]any{
			RuleFieldType: string(RuleGuard), RuleFieldSeverity: "error", RuleFieldBlocks: []any{"restart"},
			RuleFieldIf:   map[string]any{ConditionActive: map[string]any{FieldCheck: "c"}},
			RuleFieldThen: map[string]any{RuleFieldAction: string(ActionBlock), RuleFieldMessage: "busy"},
		},
	}}
	parsed, warnings := ParseRules(tree)
	byName := map[string]Rule{}
	for _, r := range parsed {
		byName[r.Name] = r
	}
	if byName["alert-high"].Severity != severity.Critical {
		t.Fatalf("declared severity = %q, want critical", byName["alert-high"].Severity)
	}
	if byName["alert-typo"].Severity.Valid() || byName["guard"].Severity.Valid() {
		t.Fatal("an invalid or guard severity survived parsing")
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want one each for the typo and the guard", warnings)
	}
}
