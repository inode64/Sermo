package rules

import (
	"context"
	"testing"
	"time"
)

func TestFormatCondition(t *testing.T) {
	cases := []struct {
		name string
		node map[string]any
		want string
	}{
		{"empty", map[string]any{}, ""},
		{"multiple operators", map[string]any{"failed": map[string]any{}, "active": map[string]any{}}, "?"},
		{"failed", map[string]any{"failed": map[string]any{"check": "http"}}, "failed:http"},
		{"active", map[string]any{"active": map[string]any{"check": "backup"}}, "active:backup"},
		{"metric name", map[string]any{"metric": map[string]any{"name": "memory"}}, "metric:memory"},
		{"metric legacy key", map[string]any{"metric": map[string]any{"metric": "memory"}}, "metric:memory"},
		{"service", map[string]any{"service": map[string]any{"service": "active"}}, "service:active"},
		{"process", map[string]any{"process": map[string]any{"name": "worker"}}, "process:worker"},
		{"file", map[string]any{"file": map[string]any{"path": "/run/app.pid"}}, "file:/run/app.pid"},
		{"command", map[string]any{"command": map[string]any{}}, "command"},
		{"changed", map[string]any{"changed": map[string]any{"path": "/etc/app.conf"}}, "changed:/etc/app.conf"},
		{"and", map[string]any{"and": []any{
			map[string]any{"failed": map[string]any{"check": "http"}},
			map[string]any{"not": map[string]any{"active": map[string]any{"check": "backup"}}},
		}}, "and(failed:http, not(active:backup))"},
		{"or", map[string]any{"or": []any{map[string]any{"command": map[string]any{}}}}, "or(command)"},
		{"invalid branch", map[string]any{"and": "nope"}, "and"},
		{"unknown", map[string]any{"unknown": map[string]any{}}, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatCondition(tc.node); got != tc.want {
				t.Fatalf("FormatCondition(%v) = %q, want %q", tc.node, got, tc.want)
			}
		})
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
			s.FiresAt(ruleSet[0], true, time.Now())
			s.FiresAt(ruleSet[0], true, time.Now())
			return s
		}(),
	}
	eval := func(_ context.Context, r Rule) (bool, error) {
		return r.Name == "restart-if-down", nil
	}
	reports := BuildRuleWindowReportsAt(context.Background(), ruleSet, windows, time.Now(), eval)
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
