package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
)

var t0 = time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

func TestEffectiveNotify(t *testing.T) {
	g := []string{"ops"}
	if got := effectiveNotify(nil, g); len(got) != 1 || got[0] != "ops" {
		t.Errorf("omitted should inherit global: %v", got)
	}
	if got := effectiveNotify([]string{"oncall"}, g); len(got) != 1 || got[0] != "oncall" {
		t.Errorf("explicit should override global: %v", got)
	}
	if got := effectiveNotify([]string{"none"}, g); got != nil {
		t.Errorf("none should suppress: %v", got)
	}
	if got := effectiveNotify([]string{"none", "oncall"}, g); got != nil {
		t.Errorf("none should win and suppress: %v", got)
	}
}

func alertRuleTree(notify any) map[string]any {
	rule := map[string]any{
		"type": "alert",
		"if":   map[string]any{"failed": map[string]any{"check": "http"}},
		"then": map[string]any{"action": "alert", "message": "http is down"},
	}
	if notify != nil {
		rule["notify"] = notify
	}
	return map[string]any{"rules": map[string]any{"warn-down": rule}}
}

// TestCycleAlertCarriesFailingCheckOutput verifies the alert event (and
// notification) carry the failing check's captured command output, so the
// operator can see why the rule fired.
func TestWorkerStartupSkipsChecksUntilBackendActive(t *testing.T) {
	h := &workerHarness{cache: failedCache("http")}
	w := h.worker(alertRuleTree(nil), rules.Policy{}, nil)
	settling := NewSettling(nil)
	settling.Reset([]string{SettlingServiceKey("web")})
	w.Settling = settling
	w.CheckDeps.Status = func(context.Context) (servicemgr.Status, error) {
		return servicemgr.StatusInactive, nil
	}

	w.RunCycle(context.Background())
	if _, ok := h.eventOf("alert"); ok {
		t.Fatal("inactive backend must not fire alerts during startup")
	}
	if !settling.Observed(SettlingServiceKey("web")) {
		t.Fatal("inactive backend must complete startup observation without running checks")
	}
}

func TestWorkerStartupObserveOnlySuppressesAlerts(t *testing.T) {
	h := &workerHarness{cache: failedCache("http")}
	w := h.worker(alertRuleTree(nil), rules.Policy{}, nil)
	settling := NewSettling(nil)
	settling.Reset([]string{SettlingServiceKey("web")})
	w.Settling = settling
	w.CheckDeps.Status = func(context.Context) (servicemgr.Status, error) {
		return servicemgr.StatusActive, nil
	}

	w.RunCycle(context.Background())
	if _, ok := h.eventOf("alert"); ok {
		t.Fatal("first active cycle must not fire alerts")
	}
	if !settling.Observed(SettlingServiceKey("web")) {
		t.Fatal("first active cycle must mark the service observed")
	}

	w.RunCycle(context.Background())
	if _, ok := h.eventOf("alert"); !ok {
		t.Fatal("second cycle should fire the alert")
	}
}

func TestWorkerOperationRunningSkipsChecksAndAlerts(t *testing.T) {
	store := newFakeStore()
	store.now = func() time.Time { return t0 }
	if err := store.SetOperationSettling("web", "restart", state.OperationSettlingRunning, state.SourceCLI); err != nil {
		t.Fatalf("SetOperationSettling: %v", err)
	}
	h := &workerHarness{cache: failedCache("http")}
	w := h.worker(alertRuleTree(nil), rules.Policy{}, nil)
	w.OperationSettling = store
	var checksRun int
	w.Checks = func(context.Context, checks.Deps) map[string]checks.Result {
		checksRun++
		return h.cache
	}

	w.RunCycle(context.Background())
	if checksRun != 0 {
		t.Fatalf("operation running must skip checks, ran %d", checksRun)
	}
	if _, ok := h.eventOf("alert"); ok {
		t.Fatal("operation running must suppress alerts")
	}
	if _, found, _ := store.OperationSettling("web"); !found {
		t.Fatal("running marker must remain until the operation result is known")
	}
}

func TestWorkerOperationSettlingObserveOnlySuppressesSideEffects(t *testing.T) {
	store := newFakeStore()
	store.now = func() time.Time { return t0 }
	if err := store.SetOperationSettling("web", "restart", state.OperationSettlingSettling, state.SourceCLI); err != nil {
		t.Fatalf("SetOperationSettling: %v", err)
	}
	h := &workerHarness{cache: failedCache("http")}
	w := h.worker(alertRuleTree(nil), rules.Policy{}, nil)
	w.OperationSettling = store
	var recorded bool
	w.RecordHealth = func(bool) { recorded = true }

	w.RunCycle(context.Background())
	if _, ok := h.eventOf("alert"); ok {
		t.Fatal("post-operation observe-only cycle must suppress alerts")
	}
	if recorded {
		t.Fatal("post-operation observe-only cycle must not record SLA")
	}
	if _, found, _ := store.OperationSettling("web"); found {
		t.Fatal("post-operation observe-only cycle must clear the marker")
	}

	w.RunCycle(context.Background())
	if _, ok := h.eventOf("alert"); !ok {
		t.Fatal("next cycle should alert when the check is still failing")
	}
}

func TestWorkerOperationSettlingWaitsForActiveBackend(t *testing.T) {
	store := newFakeStore()
	store.now = func() time.Time { return t0 }
	if err := store.SetOperationSettling("web", "start", state.OperationSettlingSettling, state.SourceCLI); err != nil {
		t.Fatalf("SetOperationSettling: %v", err)
	}
	h := &workerHarness{cache: failedCache("http")}
	w := h.worker(alertRuleTree(nil), rules.Policy{}, nil)
	w.OperationSettling = store
	w.CheckDeps.Status = func(context.Context) (servicemgr.Status, error) {
		return servicemgr.StatusInactive, nil
	}

	w.RunCycle(context.Background())
	if _, ok := h.eventOf("alert"); ok {
		t.Fatal("inactive backend while settling must suppress alerts")
	}
	if _, found, _ := store.OperationSettling("web"); !found {
		t.Fatal("settling marker must remain until the backend becomes active")
	}
}

func TestCycleAlertCarriesFailingCheckOutput(t *testing.T) {
	h := &workerHarness{cache: map[string]checks.Result{
		"http": {Check: "http", OK: false, Data: map[string]any{"output": "stderr:\nbind: address already in use"}},
	}}
	w := h.worker(alertRuleTree(nil), rules.Policy{}, nil)
	n := &fakeNotifier{name: "ops"}
	w.Notifiers = map[string]notify.Notifier{"ops": n}
	w.GlobalNotify = []string{"ops"}

	w.RunCycle(context.Background())

	ev, ok := h.eventOf("alert")
	if !ok {
		t.Fatalf("expected an alert event: %+v", h.events)
	}
	if !strings.Contains(ev.Output, "address already in use") {
		t.Fatalf("alert event must carry the failing check output, got %q", ev.Output)
	}
	if len(n.msgs) != 1 || !strings.Contains(n.msgs[0].Body, "address already in use") {
		t.Fatalf("notification body must include the output: %+v", n.msgs)
	}
}

func TestCycleAlertNotifiesGlobalDefault(t *testing.T) {
	h := &workerHarness{cache: failedCache("http")}
	w := h.worker(alertRuleTree(nil), rules.Policy{}, nil) // rule declares no notify
	n := &fakeNotifier{name: "ops"}
	w.Notifiers = map[string]notify.Notifier{"ops": n}
	w.GlobalNotify = []string{"ops"} // inherited

	w.RunCycle(context.Background())

	if len(n.msgs) != 1 {
		t.Fatalf("alert should notify the inherited global default, got %d messages", len(n.msgs))
	}
	if _, ok := h.eventOf("notify"); !ok {
		t.Errorf("expected a notify event: %+v", h.events)
	}
}

func TestCycleAlertNotifyNoneSuppresses(t *testing.T) {
	h := &workerHarness{cache: failedCache("http")}
	w := h.worker(alertRuleTree("none"), rules.Policy{}, nil) // notify: none
	n := &fakeNotifier{name: "ops"}
	w.Notifiers = map[string]notify.Notifier{"ops": n}
	w.GlobalNotify = []string{"ops"}

	w.RunCycle(context.Background())

	if len(n.msgs) != 0 {
		t.Fatalf("notify: none must suppress delivery, got %d messages", len(n.msgs))
	}
	if _, ok := h.eventOf("alert"); !ok {
		t.Errorf("alert event should still be emitted: %+v", h.events)
	}
}

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

func TestCyclePanicSuppressesRemediation(t *testing.T) {
	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(remediationTree("restart-if-down", "http", "restart"), rules.Policy{Cooldown: time.Minute}, nil)
	w.InPanic = func() bool { return true }

	w.RunCycle(context.Background())

	if len(h.ops) != 0 {
		t.Fatalf("panic mode must suppress remediation, ops=%v", h.ops)
	}
	if !w.State.LastActionAt.IsZero() {
		t.Errorf("suppressed remediation must not record state: %v", w.State.LastActionAt)
	}
	if e, ok := h.eventOf("suppressed"); !ok || !strings.Contains(e.Message, "panic mode") {
		t.Fatalf("expected a panic suppression event, got %+v", h.events)
	}
}

func TestPanicSuppressesAlertDeliveryButKeepsEvent(t *testing.T) {
	n := &fakeNotifier{name: "ops"}
	h := &workerHarness{cache: failedCache("http")}
	w := h.worker(alertRuleTree(nil), rules.Policy{}, nil)
	w.Notifiers = map[string]notify.Notifier{"ops": n}
	w.GlobalNotify = []string{"ops"}
	w.InPanic = func() bool { return true }

	w.RunCycle(context.Background())

	if len(n.msgs) != 0 {
		t.Fatalf("panic mode must suppress alert delivery, sent %d", len(n.msgs))
	}
	if _, ok := h.eventOf("alert"); !ok {
		t.Errorf("the alert event must still be emitted in panic mode: %+v", h.events)
	}
	if _, ok := h.eventOf("notify-suppressed"); !ok {
		t.Errorf("expected a notify-suppressed event: %+v", h.events)
	}
}

func TestCycleGuardCanReferencePreflightCheck(t *testing.T) {
	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
	tree := map[string]any{"rules": map[string]any{
		"block-restart-if-config-invalid": map[string]any{
			"type":   "guard",
			"blocks": []any{"restart"},
			"if":     map[string]any{"failed": map[string]any{"check": "config"}},
			"then":   map[string]any{"action": "block", "message": "config invalid"},
		},
		"restart-if-down": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"then": map[string]any{"action": "restart"},
		},
	}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
	w.ResolveRefs = func() rules.RefResolver {
		return func(_ context.Context, name string) (checks.Result, bool, error) {
			if name != "config" {
				return checks.Result{}, false, nil
			}
			return checks.Result{Check: name, OK: false}, true, nil
		}
	}

	w.RunCycle(context.Background())

	if len(h.ops) != 0 {
		t.Fatalf("guarded remediation must not operate, ops=%v", h.ops)
	}
	if e, ok := h.eventOf("suppressed"); !ok || !strings.Contains(e.Message, "guard: config invalid") {
		t.Fatalf("guard suppression event = %+v, events=%+v", e, h.events)
	}
	if e, ok := h.eventOf("error"); ok {
		t.Fatalf("preflight reference must not emit an error event: %+v", e)
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

// appVersionWorker wires a changed:{app} remediation worker over a sequenceRunner
// that returns the given version lines, one per cycle.
func appVersionWorker(h *workerHarness, runner *sequenceRunner, level string) *Worker {
	changed := map[string]any{"app": "containerd"}
	if level != "" {
		changed["level"] = level
	}
	tree := map[string]any{"rules": map[string]any{
		"restart-on-version-change": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"changed": changed},
			"then": map[string]any{"action": "restart"},
		},
	}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
	w.CheckDeps = checks.Deps{Runner: runner}
	w.appVersionCmd = map[string]appVersionCmd{"containerd": {argv: []string{"/usr/bin/containerd", "--version"}}}
	w.appVersions = map[string]string{}
	w.appVersionsLast = map[string]string{}
	return w
}

func TestCycleRestartsOnAppVersionChange(t *testing.T) {
	runner := &sequenceRunner{stdout: []string{
		"containerd v1.7.0",
		"containerd v1.7.1", // patch bump
		"containerd v1.7.1", // unchanged after the restart
	}}
	h := &workerHarness{opResult: operation.Result{Status: operation.ResultOK}}
	w := appVersionWorker(h, runner, "patch")

	// Cycle 1: first observation adopts the baseline; no restart on startup.
	w.RunCycle(context.Background())
	if len(h.ops) != 0 {
		t.Fatalf("first cycle must not restart, ops=%v", h.ops)
	}

	// Cycle 2: patch bump 1.7.0 -> 1.7.1 fires once, then baseline acknowledged.
	w.RunCycle(context.Background())
	if len(h.ops) != 1 || h.ops[0] != "restart" {
		t.Fatalf("version change should restart once, ops=%v", h.ops)
	}

	// Cycle 3: version unchanged since the restart → no further restart.
	w.RunCycle(context.Background())
	if len(h.ops) != 1 {
		t.Fatalf("acknowledged version must not refire, ops=%v", h.ops)
	}
}

func TestCycleAppVersionChangeRespectsLevel(t *testing.T) {
	// At minor level a patch bump is ignored; only a minor bump fires.
	runner := &sequenceRunner{stdout: []string{
		"containerd v1.7.0",
		"containerd v1.7.5", // patch bump — ignored at minor level
		"containerd v1.8.0", // minor bump — fires
	}}
	h := &workerHarness{opResult: operation.Result{Status: operation.ResultOK}}
	w := appVersionWorker(h, runner, "minor")

	w.RunCycle(context.Background()) // prime
	w.RunCycle(context.Background()) // patch bump
	if len(h.ops) != 0 {
		t.Fatalf("patch bump must not restart at minor level, ops=%v", h.ops)
	}
	w.RunCycle(context.Background()) // minor bump
	if len(h.ops) != 1 || h.ops[0] != "restart" {
		t.Fatalf("minor bump must restart, ops=%v", h.ops)
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
		opResult: operation.Result{Status: operation.ResultPreflightFailed, Message: "storage check failed"},
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

func TestCycleForDurationWindowDelaysAction(t *testing.T) {
	tree := map[string]any{"rules": map[string]any{
		"down": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"for":  map[string]any{"duration": "6m"},
			"then": map[string]any{"action": "restart"},
		},
	}}
	h := &workerHarness{cache: failedCache("http"), opResult: operation.Result{Status: operation.ResultOK}}
	w := h.worker(tree, rules.Policy{}, nil)
	now := t0
	w.Now = func() time.Time { return now }

	w.RunCycle(context.Background())
	now = now.Add(5 * time.Minute)
	w.RunCycle(context.Background())
	if len(h.ops) != 0 {
		t.Fatalf("must not act before duration elapses, ops=%v", h.ops)
	}
	now = now.Add(time.Minute)
	w.RunCycle(context.Background())
	if len(h.ops) != 1 || h.ops[0] != "restart" {
		t.Fatalf("ops = %v, want [restart] after 6m", h.ops)
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

// TestWorkerFiresSuppressesSystemMetricRemediation locks the second defense
// layer for safety invariant 13: even a hand-built remediation rule (one that
// never went through ParseRules) reading a scope: system metric must not
// fire — fires() suppresses it and reports the anomaly as an error event.
func TestWorkerFiresSuppressesSystemMetricRemediation(t *testing.T) {
	var events []Event
	w := &Worker{Service: "svc", Emit: func(e Event) { events = append(events, e) }}
	ev := &rules.Evaluator{Deps: checks.Deps{Metrics: func(scope, name string) (metrics.Reading, bool) {
		return metrics.Reading{Percent: 99, HasPercent: true, Ready: true}, true
	}}}
	r := rules.Rule{
		Name: "bad",
		Type: rules.RuleRemediation,
		If:   map[string]any{"metric": map[string]any{"scope": "system", "name": "total_memory", "op": ">", "value": "90%"}},
	}
	if w.fires(context.Background(), ev, r, t0, nil) {
		t.Fatal("a system-metric remediation rule must never fire")
	}
	if len(events) != 1 || events[0].Kind != "error" || !strings.Contains(events[0].Message, "alert rules") {
		t.Fatalf("events = %+v, want one error explaining the suppression", events)
	}

	events = nil
	r = rules.Rule{
		Name: "bad-inline-probe",
		Type: rules.RuleRemediation,
		If: map[string]any{"failed": map[string]any{
			"metric": map[string]any{"scope": "system", "name": "total_memory", "op": ">", "value": "90%"},
		}},
	}
	if w.fires(context.Background(), ev, r, t0, nil) {
		t.Fatal("an inline system-metric remediation probe must never fire")
	}
	if len(events) != 1 || events[0].Kind != "error" || !strings.Contains(events[0].Message, "alert rules") {
		t.Fatalf("inline events = %+v, want one error explaining the suppression", events)
	}

	events = nil
	w.MetricChecks = map[string]any{
		"machine-hot": map[string]any{"type": "metric", "scope": "system", "name": "total_cpu", "op": ">", "value": "90%"},
	}
	r = rules.Rule{
		Name: "bad-check-ref",
		Type: rules.RuleRemediation,
		If:   map[string]any{"active": map[string]any{"check": "machine-hot"}},
	}
	if w.fires(context.Background(), ev, r, t0, nil) {
		t.Fatal("a remediation rule referencing a system metric check must never fire")
	}
	if len(events) != 1 || events[0].Kind != "error" || !strings.Contains(events[0].Message, "alert rules") {
		t.Fatalf("check-ref events = %+v, want one error explaining the suppression", events)
	}

	// The same metric on an alert rule keeps working.
	r.Type = rules.RuleAlert
	r.If = map[string]any{"metric": map[string]any{"scope": "system", "name": "total_memory", "op": ">", "value": "90%"}}
	if !w.fires(context.Background(), ev, r, t0, nil) {
		t.Fatal("an alert rule on the same system metric must still fire")
	}
}

// TestWorkerRemediationReloadOperates locks the reload action end to end at
// the worker: validation accepts action: reload, so OperationAction must
// recognize it and run it through the shared engine — before this, a reload
// remediation fell into the alert-only branch and silently did nothing.
func TestWorkerRemediationReloadOperates(t *testing.T) {
	h := &workerHarness{
		cache:    failedCache("config"),
		opResult: operation.Result{Status: operation.ResultOK},
	}
	w := h.worker(remediationTree("reload-on-bad-config", "config", "reload"), rules.Policy{Cooldown: time.Minute}, nil)
	w.RunCycle(context.Background())

	if len(h.ops) != 1 || h.ops[0] != "reload" {
		t.Fatalf("ops = %v, want one reload through the engine", h.ops)
	}
	if w.State.LastActionAt.IsZero() {
		t.Fatal("an executed reload must record remediation state (cooldown)")
	}
}

// TestWorkerShadowModeEvaluatesButDoesNotAct verifies the core of the shadow
// remediation feature: conditions/windows/guards/policy are all evaluated and
// appropriate events are emitted, but no Operate is called and real
// RemediationState is not mutated.
func TestWorkerShadowModeEvaluatesButDoesNotAct(t *testing.T) {
	h := &workerHarness{cache: failedCache("http")}
	tree := remediationTree("restart-if-down", "http", "restart")
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
	w.Shadow = true

	w.RunCycle(context.Background())

	if len(h.ops) != 0 {
		t.Fatalf("shadow mode executed ops=%v; must not call Operate", h.ops)
	}
	ev, ok := h.eventOf("shadow")
	if !ok {
		t.Fatalf("no shadow event emitted; events=%+v", h.events)
	}
	if ev.Action != "restart" || !strings.Contains(ev.Message, "would") {
		t.Fatalf("shadow event = %+v, want action=restart and 'would' in message", ev)
	}
	if !w.State.LastActionAt.IsZero() || len(w.State.RecentActions) != 0 {
		t.Error("shadow must not Record remediation state (no pollution of real cooldown)")
	}
}

// TestWorkerShadowModeReportsSuppression verifies that when a firing rule would
// be blocked (here by the cooldown policy), shadow mode still emits a shadow
// event whose message records *why* the action would have been suppressed, and
// that the seeded cooldown state is left untouched.
func TestWorkerShadowModeReportsSuppression(t *testing.T) {
	h := &workerHarness{cache: failedCache("http")}
	state := &rules.RemediationState{LastActionAt: t0.Add(-30 * time.Second)} // within a 1m cooldown
	tree := remediationTree("restart-if-down", "http", "restart")
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, state)
	w.Shadow = true

	w.RunCycle(context.Background())

	if len(h.ops) != 0 {
		t.Fatalf("shadow mode executed ops=%v; must not call Operate", h.ops)
	}
	if _, ok := h.eventOf("suppressed"); ok {
		t.Fatalf("shadow mode must report via a shadow event, not a plain suppressed one; events=%+v", h.events)
	}
	ev, ok := h.eventOf("shadow")
	if !ok {
		t.Fatalf("no shadow event emitted; events=%+v", h.events)
	}
	if ev.Action != "restart" || !strings.Contains(ev.Message, "suppressed: cooldown") {
		t.Fatalf("shadow event = %+v, want action=restart and 'suppressed: cooldown' in message", ev)
	}
	if !w.State.LastActionAt.Equal(t0.Add(-30 * time.Second)) {
		t.Errorf("shadow must not mutate the seeded cooldown state, LastActionAt=%v", w.State.LastActionAt)
	}
}

func TestWorkerSettlesInactiveBackendOnObserveOnly(t *testing.T) {
	ready := NewReadiness("systemd", 1, 0)
	settling := NewSettling(ready)
	settling.Reset([]string{SettlingServiceKey("web")})
	ready.ExpectFirstCycles(1)

	var checksRan int
	w := &Worker{
		Service:  "web",
		Settling: settling,
		CheckDeps: checks.Deps{
			Status: func(context.Context) (servicemgr.Status, error) {
				return servicemgr.StatusInactive, nil
			},
		},
		Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			checksRan++
			return nil
		},
	}

	w.RunCycle(context.Background())

	if checksRan != 0 {
		t.Fatalf("inactive observe-only cycle ran checks %d times, want 0", checksRan)
	}
	if !settling.Observed(SettlingServiceKey("web")) {
		t.Fatal("inactive backend must complete startup observation")
	}
	if rep := ready.Report(context.Background()); !rep.Ready || rep.Status != "ok" {
		t.Fatalf("readiness = %+v, want ready after inactive observe-only cycle", rep)
	}
}

func TestSettlingDuplicateServiceAndAppNamesAdvanceReadiness(t *testing.T) {
	ready := NewReadiness("systemd", 2, 0)
	settling := NewSettling(ready)
	settling.Reset([]string{SettlingServiceKey("redis"), SettlingAppKey("redis")})
	ready.ExpectFirstCycles(2)

	settling.MarkObserved(SettlingServiceKey("redis"))
	settling.MarkObserved(SettlingAppKey("redis"))

	if !ready.Report(context.Background()).Ready {
		t.Fatal("service and app monitors with the same display name must both advance readiness")
	}
}
