package rules

import (
	"context"
	"testing"
)

func TestFormatCondition(t *testing.T) {
	cases := []struct {
		node map[string]any
		want string
	}{
		{map[string]any{"failed": map[string]any{"check": "http"}}, "failed:http"},
		{map[string]any{"and": []any{
			map[string]any{"failed": map[string]any{"check": "http"}},
			map[string]any{"not": map[string]any{"active": map[string]any{"check": "backup"}}},
		}}, "and(failed:http, not(active:backup))"},
	}
	for _, tc := range cases {
		if got := FormatCondition(tc.node); got != tc.want {
			t.Fatalf("FormatCondition(%v) = %q, want %q", tc.node, got, tc.want)
		}
	}
}

func TestBuildRuleWindowReports(t *testing.T) {
	tree := map[string]any{"rules": map[string]any{
		"restart-if-down": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"for":  map[string]any{"cycles": 3},
			"then": map[string]any{"action": "restart"},
		},
		"disk-alert": map[string]any{
			"type": "alert",
			"if":   map[string]any{"metric": map[string]any{"name": "disk"}},
			"then": map[string]any{"action": "alert", "message": "disk full"},
		},
		"block-backup": map[string]any{
			"type": "guard",
			"if":   map[string]any{"active": map[string]any{"check": "backup"}},
			"then": map[string]any{"action": "block", "message": "backup"},
		},
	}}
	ruleSet, _ := ParseRules(tree)
	windows := map[string]*WindowState{
		"restart-if-down": func() *WindowState {
			s := &WindowState{}
			s.Fires(ruleSet[0], true)
			s.Fires(ruleSet[0], true)
			return s
		}(),
	}
	eval := func(_ context.Context, r Rule) (bool, error) {
		return r.Name == "restart-if-down", nil
	}
	reports := BuildRuleWindowReports(ruleSet, windows, eval)
	if len(reports) != 2 {
		t.Fatalf("reports = %d, want 2 (no guard)", len(reports))
	}
	byName := map[string]RuleWindowReport{}
	for _, rep := range reports {
		byName[rep.Name] = rep
	}
	rem := byName["restart-if-down"]
	if rem.Type != "remediation" || rem.Action != "restart" {
		t.Fatalf("remediation report = %+v", rem)
	}
	if rem.Progress != "2/3" || rem.Firing || !rem.ConditionTrue {
		t.Fatalf("remediation progress = %+v, want 2/3 matching not firing", rem)
	}
	if rem.Window != "for 3 consecutive" || rem.Condition != "failed:http" {
		t.Fatalf("remediation window/condition = %+v", rem)
	}
	alert := byName["disk-alert"]
	if alert.Type != "alert" || alert.Action != "alert" {
		t.Fatalf("alert report = %+v", alert)
	}
}
