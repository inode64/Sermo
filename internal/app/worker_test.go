package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/operation"
	"sermo/internal/rules"
)

var t0 = time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

type workerHarness struct {
	cache    map[string]checks.Result
	ops      []string
	opResult operation.Result
	events   []Event
}

func (h *workerHarness) worker(tree map[string]any, policy rules.Policy, state *rules.RemediationState) *Worker {
	ruleSet, _ := rules.ParseRules(tree)
	if state == nil {
		state = &rules.RemediationState{}
	}
	return &Worker{
		Service: "web",
		Rules:   ruleSet,
		Policy:  policy,
		State:   state,
		Checks:  func(context.Context, checks.Deps) map[string]checks.Result { return h.cache },
		Operate: func(_ context.Context, action string) operation.Result {
			h.ops = append(h.ops, action)
			res := h.opResult
			res.Action = action
			return res
		},
		Now:  func() time.Time { return t0 },
		Emit: func(e Event) { h.events = append(h.events, e) },
	}
}

func (h *workerHarness) eventOf(kind string) (Event, bool) {
	for _, e := range h.events {
		if e.Kind == kind {
			return e, true
		}
	}
	return Event{}, false
}

func failedCache(check string) map[string]checks.Result {
	return map[string]checks.Result{check: {Check: check, OK: false}}
}

func remediationTree(name, check, action string) map[string]any {
	return map[string]any{"rules": map[string]any{
		name: map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": check}},
			"then": map[string]any{"action": action},
		},
	}}
}

func TestCycleFiresRemediation(t *testing.T) {
	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(remediationTree("restart-if-down", "http", "restart"), rules.Policy{Cooldown: time.Minute}, nil)

	w.RunCycle(context.Background())

	if len(h.ops) != 1 || h.ops[0] != "restart" {
		t.Fatalf("ops = %v, want [restart]", h.ops)
	}
	if !w.State.LastActionAt.Equal(t0) {
		t.Errorf("state not recorded: %v", w.State.LastActionAt)
	}
	if e, ok := h.eventOf("action"); !ok || e.Action != "restart" || e.Status != "ok" {
		t.Errorf("missing action event: %+v", h.events)
	}
}

func TestCycleNoFireWhenHealthy(t *testing.T) {
	h := &workerHarness{cache: map[string]checks.Result{"http": {Check: "http", OK: true}}}
	w := h.worker(remediationTree("restart-if-down", "http", "restart"), rules.Policy{Cooldown: time.Minute}, nil)

	w.RunCycle(context.Background())
	if len(h.ops) != 0 {
		t.Fatalf("healthy service must not act, ops=%v", h.ops)
	}
}

func TestCycleCooldownSuppresses(t *testing.T) {
	h := &workerHarness{cache: failedCache("http")}
	state := &rules.RemediationState{LastActionAt: t0.Add(-30 * time.Second)} // within a 1m cooldown
	w := h.worker(remediationTree("restart-if-down", "http", "restart"), rules.Policy{Cooldown: time.Minute}, state)

	w.RunCycle(context.Background())
	if len(h.ops) != 0 {
		t.Fatalf("cooldown must suppress the action, ops=%v", h.ops)
	}
	if e, ok := h.eventOf("suppressed"); !ok || e.Message != "cooldown" {
		t.Errorf("expected a cooldown suppression event: %+v", h.events)
	}
}

func TestCycleGuardBlocksThenNextRuleWins(t *testing.T) {
	// restart is guard-blocked; a second remediation rule (stop) is not -> stop wins.
	tree := map[string]any{"rules": map[string]any{
		"a-restart": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"then": map[string]any{"action": "restart"},
		},
		"b-stop": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"then": map[string]any{"action": "stop"},
		},
		"guard-restart": map[string]any{
			"type":   "guard",
			"blocks": []any{"restart"},
			"if":     map[string]any{"active": map[string]any{"check": "backup"}},
			"then":   map[string]any{"action": "block", "message": "backup running"},
		},
	}}
	h := &workerHarness{
		cache:    map[string]checks.Result{"http": {Check: "http", OK: false}, "backup": {Check: "backup", OK: true}},
		opResult: operation.Result{Status: operation.ResultOK},
	}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)

	w.RunCycle(context.Background())
	if len(h.ops) != 1 || h.ops[0] != "stop" {
		t.Fatalf("ops = %v, want [stop] (restart blocked, first non-blocked wins)", h.ops)
	}
	if e, ok := h.eventOf("suppressed"); !ok || e.Action != "restart" {
		t.Errorf("expected restart suppressed-by-guard event: %+v", h.events)
	}
}

func TestCycleAlertFires(t *testing.T) {
	tree := map[string]any{"rules": map[string]any{
		"warn-down": map[string]any{
			"type": "alert",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"then": map[string]any{"action": "alert", "message": "http is down"},
		},
	}}
	h := &workerHarness{cache: failedCache("http")}
	w := h.worker(tree, rules.Policy{}, nil)

	w.RunCycle(context.Background())
	if len(h.ops) != 0 {
		t.Fatalf("alert must not operate, ops=%v", h.ops)
	}
	if e, ok := h.eventOf("alert"); !ok || e.Message != "http is down" {
		t.Errorf("expected alert event: %+v", h.events)
	}
}

func TestCycleForWindowDelaysAction(t *testing.T) {
	tree := map[string]any{"rules": map[string]any{
		"down": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"for":  map[string]any{"cycles": 3},
			"then": map[string]any{"action": "restart"},
		},
	}}
	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
	// No cooldown so the only gate is the for-window.
	w := h.worker(tree, rules.Policy{}, nil)

	// Three consecutive failing cycles: no action until the third.
	w.RunCycle(context.Background())
	w.RunCycle(context.Background())
	if len(h.ops) != 0 {
		t.Fatalf("must not act before 3 consecutive failures, ops=%v", h.ops)
	}
	w.RunCycle(context.Background())
	if len(h.ops) != 1 || h.ops[0] != "restart" {
		t.Fatalf("ops = %v, want [restart] on the third failing cycle", h.ops)
	}
}

func TestCycleForWindowResetsOnRecovery(t *testing.T) {
	tree := map[string]any{"rules": map[string]any{
		"down": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"for":  map[string]any{"cycles": 2},
			"then": map[string]any{"action": "restart"},
		},
	}}
	h := &workerHarness{opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(tree, rules.Policy{}, nil)

	h.cache = failedCache("http")
	w.RunCycle(context.Background()) // fail 1
	h.cache = map[string]checks.Result{"http": {Check: "http", OK: true}}
	w.RunCycle(context.Background()) // healthy -> streak resets
	h.cache = failedCache("http")
	w.RunCycle(context.Background()) // fail 1 again, not 2 yet
	if len(h.ops) != 0 {
		t.Fatalf("recovery must reset the streak, ops=%v", h.ops)
	}
}

func TestCycleBackoffGrowsAndRecovers(t *testing.T) {
	tree := remediationTree("restart-if-down", "http", "restart")
	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
	policy := rules.Policy{Cooldown: time.Minute, Backoff: &rules.Backoff{Initial: 2 * time.Minute, Factor: 2}}
	state := &rules.RemediationState{}
	w := h.worker(tree, policy, state)

	// First failing cycle acts and arms the backoff.
	w.RunCycle(context.Background())
	if len(h.ops) != 1 || state.CurrentBackoff != 2*time.Minute {
		t.Fatalf("after first action: ops=%v backoff=%v", h.ops, state.CurrentBackoff)
	}

	// A healthy cycle resets the backoff.
	h.cache = map[string]checks.Result{"http": {Check: "http", OK: true}}
	w.RunCycle(context.Background())
	if state.CurrentBackoff != 0 {
		t.Fatalf("healthy cycle should reset backoff, got %v", state.CurrentBackoff)
	}
}

func TestCycleMultiActionRunsAlertThenOperation(t *testing.T) {
	tree := map[string]any{"rules": map[string]any{
		"down": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"then": map[string]any{"actions": []any{
				map[string]any{"type": "alert", "message": "http is down, restarting"},
				map[string]any{"type": "restart"},
			}},
		},
	}}
	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)

	w.RunCycle(context.Background())

	if len(h.ops) != 1 || h.ops[0] != "restart" {
		t.Fatalf("ops = %v, want [restart]", h.ops)
	}
	if e, ok := h.eventOf("alert"); !ok || e.Message != "http is down, restarting" {
		t.Fatalf("expected the alert action to also fire: %+v", h.events)
	}
	if _, ok := h.eventOf("action"); !ok {
		t.Fatalf("expected the restart action event")
	}
}

func TestCycleMultiActionSuppressedDoesNotAlert(t *testing.T) {
	// When the operation is suppressed by cooldown, the rule's alert does not
	// fire either (no alert spam every cycle).
	tree := map[string]any{"rules": map[string]any{
		"down": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"then": map[string]any{"actions": []any{
				map[string]any{"type": "alert", "message": "down"},
				map[string]any{"type": "restart"},
			}},
		},
	}}
	h := &workerHarness{cache: failedCache("http")}
	state := &rules.RemediationState{LastActionAt: t0.Add(-30 * time.Second)}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, state)

	w.RunCycle(context.Background())
	if len(h.ops) != 0 {
		t.Fatalf("cooldown must suppress, ops=%v", h.ops)
	}
	if _, ok := h.eventOf("alert"); ok {
		t.Fatalf("alert must not fire while suppressed: %+v", h.events)
	}
}

func TestCycleAtMostOneRemediation(t *testing.T) {
	tree := map[string]any{"rules": map[string]any{
		"a": map[string]any{"type": "remediation", "if": map[string]any{"failed": map[string]any{"check": "http"}}, "then": map[string]any{"action": "restart"}},
		"b": map[string]any{"type": "remediation", "if": map[string]any{"failed": map[string]any{"check": "http"}}, "then": map[string]any{"action": "restart"}},
	}}
	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)

	w.RunCycle(context.Background())
	if len(h.ops) != 1 {
		t.Fatalf("at most one remediation per cycle, ops=%v", h.ops)
	}
}
