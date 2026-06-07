package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/rules"
)

func TestWebBackendDetailRuleWindows(t *testing.T) {
	reg := NewRuleWindowRegistry()
	reg.Publish("web", []rules.RuleWindowReport{{
		Name:          "restart-if-down",
		Type:          "remediation",
		Action:        "restart",
		Condition:     "failed:http",
		ConditionTrue: true,
		Window:        "for 3 consecutive",
		Progress:      "2/3",
		Firing:        false,
	}})

	b := &WebBackend{
		order:       []string{"web"},
		entries:     map[string]*webEntry{"web": {}},
		ruleWindows: reg,
	}

	detail, ok := b.Detail(context.Background(), "web")
	if !ok {
		t.Fatal("detail not found")
	}
	if len(detail.Rules) != 1 {
		t.Fatalf("rules = %+v, want one entry", detail.Rules)
	}
	r := detail.Rules[0]
	if r.Name != "restart-if-down" || r.Progress != "2/3" || !r.ConditionTrue || r.Firing {
		t.Fatalf("rule = %+v", r)
	}
}

func TestWorkerPublishesRuleWindows(t *testing.T) {
	reg := NewRuleWindowRegistry()
	h := &workerHarness{cache: failedCache("http")}
	tree := map[string]any{"rules": map[string]any{
		"restart-if-down": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"for":  map[string]any{"cycles": 3},
			"then": map[string]any{"action": "restart"},
		},
	}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
	w.RuleWindows = reg
	w.RunCycle(context.Background())

	reports, ok := reg.Get("web")
	if !ok {
		t.Fatal("rule windows not published")
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %+v", reports)
	}
	if reports[0].Progress != "1/3" || !reports[0].ConditionTrue || reports[0].Firing {
		t.Fatalf("report = %+v, want 1/3 matching not firing", reports[0])
	}
}