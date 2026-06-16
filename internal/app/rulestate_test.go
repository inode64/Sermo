package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/state"
)

func TestLoadedRemediationStateSuppressesAfterRestart(t *testing.T) {
	store := openRuleStateStore(t)
	if err := store.SetRemediationState("web", state.RemediationRecord{
		LastActionAt:   t0.Add(-30 * time.Second),
		RecentActions:  []time.Time{t0.Add(-30 * time.Second)},
		CurrentBackoff: 0,
	}); err != nil {
		t.Fatalf("SetRemediationState: %v", err)
	}

	tree := remediationTree("restart-if-down", "http", "restart")
	ruleSet, _ := rules.ParseRules(tree)
	remediation, windows, warnings := loadRuleState(store, "web", ruleSet)
	if len(warnings) != 0 {
		t.Fatalf("load warnings: %v", warnings)
	}

	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, remediation)
	w.windows = windows
	w.RunCycle(context.Background())

	if len(h.ops) != 0 {
		t.Fatalf("persisted cooldown must suppress after restart, ops=%v", h.ops)
	}
	if e, ok := h.eventOf("suppressed"); !ok || e.Message != "cooldown" {
		t.Fatalf("expected cooldown suppression from persisted state, events=%+v", h.events)
	}
}

func TestLoadedRuleWindowStateFiresAfterRestart(t *testing.T) {
	store := openRuleStateStore(t)
	if err := store.SetRuleWindowStates("web", map[string]state.RuleWindowRecord{
		"restart-if-down": {Consecutive: 2},
	}); err != nil {
		t.Fatalf("SetRuleWindowStates: %v", err)
	}

	tree := map[string]any{"rules": map[string]any{
		"restart-if-down": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"for":  map[string]any{"cycles": 3},
			"then": map[string]any{"action": "restart"},
		},
	}}
	ruleSet, _ := rules.ParseRules(tree)
	remediation, windows, warnings := loadRuleState(store, "web", ruleSet)
	if len(warnings) != 0 {
		t.Fatalf("load warnings: %v", warnings)
	}

	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(tree, rules.Policy{}, remediation)
	w.windows = windows
	w.RunCycle(context.Background())

	if len(h.ops) != 1 || h.ops[0] != "restart" {
		t.Fatalf("persisted 2/3 window must fire on next failed cycle, ops=%v", h.ops)
	}
}

func TestWorkerPersistsRuleState(t *testing.T) {
	store := openRuleStateStore(t)
	tree := map[string]any{"rules": map[string]any{
		"restart-if-down": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"for":  map[string]any{"cycles": 3},
			"then": map[string]any{"action": "restart"},
		},
	}}
	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(tree, rules.Policy{}, nil)
	w.PersistState = ruleStatePersister(store, w.Emit, w.Service, w.Rules)

	w.RunCycle(context.Background())

	windows, err := store.RuleWindowStates("web")
	if err != nil {
		t.Fatalf("RuleWindowStates: %v", err)
	}
	if got := windows["restart-if-down"].Consecutive; got != 1 {
		t.Fatalf("persisted consecutive = %d, want 1", got)
	}
	if _, found, err := store.RemediationState("web"); err != nil || found {
		t.Fatalf("remediation state found=%v err=%v, want no cooldown row before action", found, err)
	}
}

func openRuleStateStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), state.Filename))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
