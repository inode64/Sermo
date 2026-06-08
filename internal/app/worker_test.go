package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestCycleRestartsOnLibraryChange(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "libc.so.6")
	if err := os.WriteFile(lib, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &workerHarness{opResult: operation.Result{Status: operation.ResultOK}}
	tree := map[string]any{"rules": map[string]any{
		"restart-on-change-glibc": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"changed": map[string]any{"path": lib}},
			"then": map[string]any{"action": "restart"},
		},
	}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)

	// Cycle 1: first observation adopts the baseline; no restart on startup.
	w.RunCycle(context.Background())
	if len(h.ops) != 0 {
		t.Fatalf("first cycle must not restart, ops=%v", h.ops)
	}

	// The library is upgraded (different size and mtime).
	if err := os.WriteFile(lib, []byte("v2-larger"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(lib, future, future); err != nil {
		t.Fatal(err)
	}

	// Cycle 2: change detected → one restart, then baseline acknowledged.
	w.RunCycle(context.Background())
	if len(h.ops) != 1 || h.ops[0] != "restart" {
		t.Fatalf("change should restart once, ops=%v", h.ops)
	}

	// Cycle 3: nothing changed since the restart → no further restart.
	w.RunCycle(context.Background())
	if len(h.ops) != 1 {
		t.Fatalf("acknowledged change must not refire, ops=%v", h.ops)
	}
}

func TestFailedOperationEmitsErrorEvent(t *testing.T) {
	h := &workerHarness{
		cache:    failedCache("http"),
		opResult: operation.Result{Status: operation.ResultFailed, Message: "systemctl failed"},
	}
	w := h.worker(remediationTree("restart-if-down", "http", "restart"), rules.Policy{Cooldown: time.Minute}, nil)

	w.RunCycle(context.Background())

	if e, ok := h.eventOf("error"); !ok || e.Action != "restart" || e.Status != "failed" {
		t.Fatalf("failed remediation event = %+v, want kind=error status=failed", h.events)
	}
	if _, ok := h.eventOf("action"); ok {
		t.Fatalf("failed operation must not emit kind=action: %+v", h.events)
	}
}

func TestBlockedOperationEmitsSuppressedEvent(t *testing.T) {
	h := &workerHarness{
		cache:    failedCache("http"),
		opResult: operation.Result{Status: operation.ResultBlocked, Message: "lock held"},
	}
	w := h.worker(remediationTree("restart-if-down", "http", "restart"), rules.Policy{Cooldown: time.Minute}, nil)

	w.RunCycle(context.Background())

	if e, ok := h.eventOf("suppressed"); !ok || e.Action != "restart" || e.Status != "blocked" {
		t.Fatalf("blocked remediation event = %+v, want kind=suppressed status=blocked", h.events)
	}
	if _, ok := h.eventOf("action"); ok {
		t.Fatalf("blocked operation must not emit kind=action: %+v", h.events)
	}
}

func TestBlockedOperationDoesNotRecordCooldown(t *testing.T) {
	h := &workerHarness{
		cache:    failedCache("http"),
		opResult: operation.Result{Status: operation.ResultBlocked, Message: "lock held"},
	}
	w := h.worker(remediationTree("restart-if-down", "http", "restart"), rules.Policy{Cooldown: time.Minute}, nil)

	w.RunCycle(context.Background())

	if len(h.ops) != 1 {
		t.Fatalf("ops = %v, want [restart]", h.ops)
	}
	if !w.State.LastActionAt.IsZero() {
		t.Fatalf("blocked operation must not record cooldown, LastActionAt=%v", w.State.LastActionAt)
	}
}

func TestPreflightFailedOperationDoesNotRecordCooldown(t *testing.T) {
	h := &workerHarness{
		cache:    failedCache("http"),
		opResult: operation.Result{Status: operation.ResultPreflightFailed, Message: "disk check failed"},
	}
	w := h.worker(remediationTree("restart-if-down", "http", "restart"), rules.Policy{Cooldown: time.Minute}, nil)

	w.RunCycle(context.Background())

	if !w.State.LastActionAt.IsZero() {
		t.Fatalf("preflight failure must not record cooldown, LastActionAt=%v", w.State.LastActionAt)
	}
}

func TestFailedOperationRecordsCooldown(t *testing.T) {
	h := &workerHarness{
		cache:    failedCache("http"),
		opResult: operation.Result{Status: operation.ResultFailed, Message: "systemctl failed"},
	}
	w := h.worker(remediationTree("restart-if-down", "http", "restart"), rules.Policy{Cooldown: time.Minute}, nil)

	w.RunCycle(context.Background())

	if !w.State.LastActionAt.Equal(t0) {
		t.Fatalf("executed-but-failed remediation should record cooldown, LastActionAt=%v", w.State.LastActionAt)
	}
}

func TestBlockedOperationAllowsImmediateRetry(t *testing.T) {
	h := &workerHarness{
		cache:    failedCache("http"),
		opResult: operation.Result{Status: operation.ResultBlocked, Message: "lock held"},
	}
	policy := rules.Policy{Cooldown: time.Minute}
	w := h.worker(remediationTree("restart-if-down", "http", "restart"), policy, nil)

	w.RunCycle(context.Background())
	h.opResult = operation.Result{Status: operation.ResultOK}
	w.RunCycle(context.Background())

	if len(h.ops) != 2 {
		t.Fatalf("ops = %v, want two restart attempts", h.ops)
	}
	if !w.State.LastActionAt.Equal(t0) {
		t.Fatalf("only the successful attempt should record cooldown, LastActionAt=%v", w.State.LastActionAt)
	}
}

func TestCyclePausedDoesNothing(t *testing.T) {
	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(remediationTree("restart-if-down", "http", "restart"), rules.Policy{Cooldown: time.Minute}, nil)
	w.IsPaused = func() bool { return true }

	w.RunCycle(context.Background())

	if len(h.ops) != 0 {
		t.Errorf("paused worker must run no actions, ops=%v", h.ops)
	}
	if len(h.events) != 0 {
		t.Errorf("paused worker must emit nothing, events=%v", h.events)
	}
}

func TestPausedCycleAdvancesWithoutChecks(t *testing.T) {
	checksCalled := 0
	w := &Worker{
		IsPaused: func() bool { return true },
		Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			checksCalled++
			return nil
		},
	}
	for i := 0; i < 3; i++ {
		w.RunCycle(context.Background())
	}
	if checksCalled != 0 {
		t.Fatalf("paused cycles must not run checks, called %d times", checksCalled)
	}
	if w.cycle != 3 {
		t.Fatalf("cycle = %d, want 3 after three paused ticks", w.cycle)
	}
}

func TestRuntimeVarsSubstitutedInMessage(t *testing.T) {
	h := &workerHarness{cache: failedCache("http")}
	tree := map[string]any{"rules": map[string]any{
		"notify": map[string]any{
			"type": "alert",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"then": map[string]any{"action": "alert", "message": "${service} ${event} at ${date}"},
		},
	}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)

	w.RunCycle(context.Background())

	e, ok := h.eventOf("alert")
	if !ok {
		t.Fatalf("no alert emitted: %+v", h.events)
	}
	if strings.Contains(e.Message, "${") {
		t.Errorf("runtime vars not substituted: %q", e.Message)
	}
	want := "web notify at " + t0.Format(time.RFC3339)
	if e.Message != want {
		t.Errorf("message = %q, want %q", e.Message, want)
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

func TestCycleCooldownSkipsToNextFiringRule(t *testing.T) {
	// restart is in cooldown; a later alert-only remediation rule still notifies.
	tree := map[string]any{"rules": map[string]any{
		"a-restart": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"then": map[string]any{"action": "restart"},
		},
		"b-notify": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"then": map[string]any{"action": "alert", "message": "http still down"},
		},
	}}
	h := &workerHarness{cache: failedCache("http")}
	state := &rules.RemediationState{LastActionAt: t0.Add(-30 * time.Second)}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, state)

	w.RunCycle(context.Background())
	if len(h.ops) != 0 {
		t.Fatalf("cooldown must suppress restart, ops=%v", h.ops)
	}
	if e, ok := h.eventOf("suppressed"); !ok || e.Rule != "a-restart" {
		t.Fatalf("expected restart suppressed-by-cooldown: %+v", h.events)
	}
	if e, ok := h.eventOf("alert"); !ok || e.Rule != "b-notify" || e.Message != "http still down" {
		t.Fatalf("later firing rule must still alert: %+v", h.events)
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
