package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/emission"
	"sermo/internal/execx"
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
	if _, ok := h.eventOf(eventKindAlert); ok {
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
	if _, ok := h.eventOf(eventKindAlert); ok {
		t.Fatal("first active cycle must not fire alerts")
	}
	if !settling.Observed(SettlingServiceKey("web")) {
		t.Fatal("first active cycle must mark the service observed")
	}

	w.RunCycle(context.Background())
	if _, ok := h.eventOf(eventKindAlert); !ok {
		t.Fatal("second cycle should fire the alert")
	}
}

func TestWorkerMarksObservabilityReadyAfterNormalStartupCycle(t *testing.T) {
	h := &workerHarness{cache: map[string]checks.Result{}}
	w := h.worker(nil, rules.Policy{}, nil)
	settling := NewSettling(nil)
	settling.Reset([]string{SettlingServiceKey("web")})
	observability := NewObservabilityRegistry()
	now := t0
	w.Settling = settling
	w.Observability = observability
	w.Now = func() time.Time { return now }

	w.RunCycle(context.Background())
	if _, ready := observability.Ready("web"); ready {
		t.Fatal("startup observe-only cycle must not mark observability ready")
	}

	now = now.Add(time.Second)
	w.RunCycle(context.Background())
	at, ready := observability.Ready("web")
	if !ready || !at.Equal(now) {
		t.Fatalf("observability ready = %v at %s, want ready at %s", ready, at, now)
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
	if _, ok := h.eventOf(eventKindAlert); ok {
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
	if _, ok := h.eventOf(eventKindAlert); ok {
		t.Fatal("post-operation observe-only cycle must suppress alerts")
	}
	if recorded {
		t.Fatal("post-operation observe-only cycle must not record SLA")
	}
	if _, found, _ := store.OperationSettling("web"); found {
		t.Fatal("post-operation observe-only cycle must clear the marker")
	}

	w.RunCycle(context.Background())
	if _, ok := h.eventOf(eventKindAlert); !ok {
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
	if _, ok := h.eventOf(eventKindAlert); ok {
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

	ev, ok := h.eventOf(eventKindAlert)
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
	if _, ok := h.eventOf(eventKindNotify); !ok {
		t.Errorf("expected a notify event: %+v", h.events)
	}
}

func TestCycleAlertEmitsOnChangeByDefault(t *testing.T) {
	h := &workerHarness{cache: failedCache("http")}
	w := h.worker(alertRuleTree(nil), rules.Policy{}, nil)
	n := &fakeNotifier{name: "ops"}
	w.Notifiers = map[string]notify.Notifier{"ops": n}
	w.GlobalNotify = []string{"ops"}

	w.RunCycle(context.Background())
	w.RunCycle(context.Background())
	h.cache = map[string]checks.Result{"http": {Check: "http", OK: true}}
	w.RunCycle(context.Background())

	if got := h.countEvents(eventKindAlert); got != 1 {
		t.Fatalf("default alert rule must emit once per firing episode, got %d events: %+v", got, h.events)
	}
	if got := h.countEvents(eventKindNotify); got != 1 {
		t.Fatalf("default alert rule must notify once per firing episode, got %d events: %+v", got, h.events)
	}
	if len(n.msgs) != 1 {
		t.Fatalf("default alert rule must send one notification per episode, got %d messages", len(n.msgs))
	}
	if got := h.countEvents(eventKindRecovered); got != 1 {
		t.Fatalf("recovery must still emit a recovered event, got %d events: %+v", got, h.events)
	}
}

func TestCycleAlertEmissionEveryCycleRepeats(t *testing.T) {
	tree := alertRuleTree(nil)
	rule := tree["rules"].(map[string]any)["warn-down"].(map[string]any)
	rule[emission.Section] = map[string]any{
		emission.KeyEvents: emission.ModeEveryCycle,
		emission.KeyNotify: emission.ModeEveryCycle,
	}
	h := &workerHarness{cache: failedCache("http")}
	w := h.worker(tree, rules.Policy{}, nil)
	n := &fakeNotifier{name: "ops"}
	w.Notifiers = map[string]notify.Notifier{"ops": n}
	w.GlobalNotify = []string{"ops"}

	w.RunCycle(context.Background())
	w.RunCycle(context.Background())

	if got := h.countEvents(eventKindAlert); got != 2 {
		t.Fatalf("every-cycle alert rule must emit every cycle, got %d events: %+v", got, h.events)
	}
	if len(n.msgs) != 2 {
		t.Fatalf("every-cycle alert rule must notify every cycle, got %d messages", len(n.msgs))
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
	if _, ok := h.eventOf(eventKindAlert); !ok {
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
		Service:      "web",
		Rules:        ruleSet,
		Policy:       policy,
		State:        state,
		MetricChecks: rules.ReferencedChecks(tree),
		Checks:       func(context.Context, checks.Deps) map[string]checks.Result { return h.cache },
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

func (h *workerHarness) countEvents(kind string) int {
	var count int
	for _, e := range h.events {
		if e.Kind == kind {
			count++
		}
	}
	return count
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

	if len(h.ops) != 1 || h.ops[0] != string(rules.ActionRestart) {
		t.Fatalf("ops = %v, want [restart]", h.ops)
	}
	if !w.State.LastActionAt.Equal(t0) {
		t.Errorf("state not recorded: %v", w.State.LastActionAt)
	}
	if e, ok := h.eventOf(eventKindAction); !ok || e.Action != string(rules.ActionRestart) || e.Status != eventStatusOK {
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
	if e, ok := h.eventOf(eventKindSuppressed); !ok || !strings.Contains(e.Message, "panic mode") {
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
	if _, ok := h.eventOf(eventKindAlert); !ok {
		t.Errorf("the alert event must still be emitted in panic mode: %+v", h.events)
	}
	if _, ok := h.eventOf(eventKindNotifySuppressed); !ok {
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
	if e, ok := h.eventOf(eventKindSuppressed); !ok || !strings.Contains(e.Message, "guard: config invalid") {
		t.Fatalf("guard suppression event = %+v, events=%+v", e, h.events)
	}
	if e, ok := h.eventOf(eventKindError); ok {
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
	if len(h.ops) != 1 || h.ops[0] != string(rules.ActionRestart) {
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
	if len(h.ops) != 1 || h.ops[0] != string(rules.ActionRestart) {
		t.Fatalf("version change should restart once, ops=%v", h.ops)
	}

	// Cycle 3: version unchanged since the restart → no further restart.
	w.RunCycle(context.Background())
	if len(h.ops) != 1 {
		t.Fatalf("acknowledged version must not refire, ops=%v", h.ops)
	}
}

func TestCycleAppVersionChangeUsesArtifactSample(t *testing.T) {
	runner := &sequenceRunner{stdout: []string{"containerd v9.9.9"}}
	h := &workerHarness{opResult: operation.Result{Status: operation.ResultOK}}
	w := appVersionWorker(h, runner, "patch")
	samples := NewArtifactSamples()
	samples.RegisterApp("containerd")
	w.artifactSamples = samples

	samples.StoreAppVersion("containerd", "containerd v1.7.0", nil)
	w.RunCycle(context.Background()) // establish baseline from the artifact cache
	samples.StoreAppVersion("containerd", "containerd v1.7.1", nil)
	w.RunCycle(context.Background())

	if len(h.ops) != 1 || h.ops[0] != string(rules.ActionRestart) {
		t.Fatalf("cached app change should restart once, ops=%v", h.ops)
	}
	if runner.calls != 0 {
		t.Fatalf("worker must not re-run cached app version command, calls=%d", runner.calls)
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
	if len(h.ops) != 1 || h.ops[0] != string(rules.ActionRestart) {
		t.Fatalf("minor bump must restart, ops=%v", h.ops)
	}
}

type resultSequenceRunner struct {
	results []execx.Result
	calls   int
}

func (r *resultSequenceRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	if len(r.results) == 0 {
		return execx.Result{ExitCode: execx.ExitCodeSuccess}, nil
	}
	idx := r.calls
	if idx >= len(r.results) {
		idx = len(r.results) - 1
	}
	r.calls++
	return r.results[idx], nil
}

func TestCycleAppVersionCommandFailureDoesNotRestartOrAcknowledge(t *testing.T) {
	runner := &resultSequenceRunner{results: []execx.Result{
		{Stdout: "containerd v1.7.0", ExitCode: execx.ExitCodeSuccess},
		{Stderr: "missing shared library", ExitCode: 127},
		{Stdout: "containerd v1.7.1", ExitCode: execx.ExitCodeSuccess},
		{Stdout: "containerd v1.7.1", ExitCode: execx.ExitCodeSuccess},
	}}
	h := &workerHarness{opResult: operation.Result{Status: operation.ResultOK}}
	w := appVersionWorker(h, nil, "patch")
	w.CheckDeps = checks.Deps{Runner: runner}

	w.RunCycle(context.Background()) // prime baseline at 1.7.0
	w.RunCycle(context.Background()) // broken binary/version command
	if len(h.ops) != 0 {
		t.Fatalf("broken version command must not restart, ops=%v", h.ops)
	}
	if _, ok := h.eventOf(eventKindError); !ok {
		t.Fatalf("broken version command should emit an error event, events=%+v", h.events)
	}

	w.RunCycle(context.Background()) // valid 1.7.1 still differs from 1.7.0
	if len(h.ops) != 1 || h.ops[0] != string(rules.ActionRestart) {
		t.Fatalf("failed version sample must not acknowledge baseline, ops=%v", h.ops)
	}

	w.RunCycle(context.Background()) // acknowledged by successful restart
	if len(h.ops) != 1 {
		t.Fatalf("acknowledged version must not refire, ops=%v", h.ops)
	}
}

func TestFailedOperationEmitsErrorEvent(t *testing.T) {
	h := &workerHarness{
		cache:    failedCache("http"),
		opResult: operation.Result{Status: operation.ResultFailed, Message: "systemctl failed"},
	}
	w := h.worker(remediationTree("restart-if-down", "http", "restart"), rules.Policy{Cooldown: time.Minute}, nil)

	w.RunCycle(context.Background())

	if e, ok := h.eventOf(eventKindError); !ok || e.Action != string(rules.ActionRestart) || e.Status != eventStatusFailed {
		t.Fatalf("failed remediation event = %+v, want kind=error status=failed", h.events)
	}
	if _, ok := h.eventOf(eventKindAction); ok {
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

	if e, ok := h.eventOf(eventKindSuppressed); !ok || e.Action != string(rules.ActionRestart) || e.Status != eventStatusBlocked {
		t.Fatalf("blocked remediation event = %+v, want kind=suppressed status=blocked", h.events)
	}
	if _, ok := h.eventOf(eventKindAction); ok {
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
	for range 3 {
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

	e, ok := h.eventOf(eventKindAlert)
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

func TestRuleMessageRuntimeContextForChangedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "web.conf")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &workerHarness{opResult: operation.Result{Status: operation.ResultOK}}
	tree := map[string]any{"rules": map[string]any{
		"restart-on-change": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"changed": map[string]any{"path": path, "library": "glibc"}},
			"then": map[string]any{"actions": []any{
				map[string]any{"type": "alert", "message": "${change.library} changed at ${change.path}"},
				map[string]any{"type": "restart"},
			}},
		},
	}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)

	w.RunCycle(context.Background())
	if _, ok := h.eventOf(eventKindAlert); ok {
		t.Fatal("baseline cycle must not alert")
	}
	if err := os.WriteFile(path, []byte("v2-larger"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	w.RunCycle(context.Background())

	e, ok := h.eventOf(eventKindAlert)
	if !ok {
		t.Fatalf("no alert emitted: %+v", h.events)
	}
	want := "glibc changed at " + path
	if e.Message != want {
		t.Fatalf("message = %q, want %q", e.Message, want)
	}
}

func TestRuleMessageRuntimeContextForChangedAppVersion(t *testing.T) {
	runner := &sequenceRunner{stdout: []string{
		"containerd v1.7.0",
		"containerd v1.7.1",
	}}
	h := &workerHarness{opResult: operation.Result{Status: operation.ResultOK}}
	tree := map[string]any{"rules": map[string]any{
		"restart-on-version-change": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"changed": map[string]any{"app": "containerd", "level": "patch"}},
			"then": map[string]any{"actions": []any{
				map[string]any{"type": "alert", "message": "${change.app} ${change.level} ${change.old_version} -> ${change.new_version}"},
				map[string]any{"type": "restart"},
			}},
		},
	}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
	w.CheckDeps = checks.Deps{Runner: runner}
	w.appVersionCmd = map[string]appVersionCmd{"containerd": {argv: []string{"/usr/bin/containerd", "--version"}}}
	w.appVersions = map[string]string{}
	w.appVersionsLast = map[string]string{}

	w.RunCycle(context.Background())
	if _, ok := h.eventOf(eventKindAlert); ok {
		t.Fatal("baseline cycle must not alert")
	}
	w.RunCycle(context.Background())

	e, ok := h.eventOf(eventKindAlert)
	if !ok {
		t.Fatalf("no alert emitted: %+v", h.events)
	}
	const want = "containerd patch 1.7.0 -> 1.7.1"
	if e.Message != want {
		t.Fatalf("message = %q, want %q", e.Message, want)
	}
}

func TestRuleMessageRuntimeContextForMetricCheck(t *testing.T) {
	h := &workerHarness{cache: map[string]checks.Result{
		"mem": {
			Check: "mem",
			OK:    true,
			Data: map[string]any{
				checks.DataKeyType:      checks.CheckTypeMetric,
				checks.DataKeyScope:     checks.MetricScopeService,
				checks.DataKeyMetric:    "memory",
				checks.DataKeyOp:        ">",
				checks.DataKeyThreshold: "60%",
				checks.DataKeyValue:     73.5,
				checks.DataKeyUnit:      metrics.MetricUnitPercent,
			},
		},
	}}
	tree := map[string]any{"rules": map[string]any{
		"memory-high": map[string]any{
			"type": "alert",
			"if":   map[string]any{"active": map[string]any{"check": "mem"}},
			"for":  map[string]any{"duration": "10m"},
			"then": map[string]any{
				"action":  "alert",
				"message": "During ${rule.duration} (${rule.window}) ${check.name} ${check.type}/${check.metric} ${check.op} ${check.threshold} current ${check.value} on ${service} via ${action} at ${date}",
			},
		},
	}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
	now := t0
	w.Now = func() time.Time { return now }
	n := &fakeNotifier{name: "ops"}
	w.Notifiers = map[string]notify.Notifier{"ops": n}
	w.GlobalNotify = []string{"ops"}
	w.GlobalEmission = emission.Policy{Events: emission.ModeEveryCycle, Notify: emission.ModeEveryCycle}

	w.RunCycle(context.Background())
	if _, ok := h.eventOf(eventKindAlert); ok {
		t.Fatal("duration window must not fire on the first true cycle")
	}
	now = now.Add(10 * time.Minute)
	w.RunCycle(context.Background())

	want := "During 10m (for 10m) mem metric/memory > 60% current 73,5% on web via alert at " + now.Format(time.RFC3339)
	e, ok := h.eventOf(eventKindAlert)
	if !ok {
		t.Fatalf("no alert emitted: %+v", h.events)
	}
	if e.Message != want {
		t.Fatalf("event message = %q, want %q", e.Message, want)
	}
	if len(n.msgs) != 1 {
		t.Fatalf("notifier messages = %d, want 1", len(n.msgs))
	}
	if n.msgs[0].Body != want {
		t.Fatalf("notification body = %q, want %q", n.msgs[0].Body, want)
	}
	if strings.Contains(n.msgs[0].Subject, "${") {
		t.Fatalf("notification subject kept placeholders: %q", n.msgs[0].Subject)
	}
}

func TestRuleMessageRuntimeContextForInlineMetric(t *testing.T) {
	h := &workerHarness{cache: map[string]checks.Result{}}
	tree := map[string]any{"rules": map[string]any{
		"memory-high": map[string]any{
			"type": "alert",
			"if": map[string]any{"metric": map[string]any{
				"scope": checks.MetricScopeService,
				"name":  "memory",
				"op":    ">",
				"value": "60%",
			}},
			"then": map[string]any{"action": "alert", "message": "${check.type}/${check.metric} ${check.op} ${check.threshold}: ${check.value}"},
		},
	}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
	w.CheckDeps.Metrics = func(scope, name string) (metrics.Reading, bool) {
		if scope != checks.MetricScopeService || name != "memory" {
			return metrics.Reading{}, false
		}
		return metrics.Reading{Percent: 66.25, HasPercent: true, Ready: true}, true
	}

	w.RunCycle(context.Background())

	e, ok := h.eventOf(eventKindAlert)
	if !ok {
		t.Fatalf("no alert emitted: %+v", h.events)
	}
	if want := "metric/memory > 60%: 66,25%"; e.Message != want {
		t.Fatalf("message = %q, want %q", e.Message, want)
	}
}

func TestRuleMessageRuntimeContextFormatsInlineByteMetric(t *testing.T) {
	h := &workerHarness{cache: map[string]checks.Result{}}
	tree := map[string]any{"rules": map[string]any{
		"memory-high": map[string]any{
			"type": "alert",
			"if": map[string]any{"metric": map[string]any{
				"scope": checks.MetricScopeService,
				"name":  "memory",
				"op":    ">",
				"value": "1741594",
			}},
			"then": map[string]any{"action": "alert", "message": "${check.type}/${check.metric} ${check.op} ${check.threshold}: ${check.value}"},
		},
	}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
	w.CheckDeps.Metrics = func(scope, name string) (metrics.Reading, bool) {
		if scope != checks.MetricScopeService || name != "memory" {
			return metrics.Reading{}, false
		}
		return metrics.Reading{Absolute: 2555904, Unit: metrics.MetricUnitBytes, HasAbsolute: true, Ready: true}, true
	}

	w.RunCycle(context.Background())

	e, ok := h.eventOf(eventKindAlert)
	if !ok {
		t.Fatalf("no alert emitted: %+v", h.events)
	}
	if want := "metric/memory > 1,66 MB: 2,44 MB"; e.Message != want {
		t.Fatalf("message = %q, want %q", e.Message, want)
	}
}

func TestRuleMessageRuntimeContextFormatsByteMetric(t *testing.T) {
	h := &workerHarness{cache: map[string]checks.Result{
		"memory-high": {
			Check: "memory-high",
			OK:    true,
			Data: map[string]any{
				checks.DataKeyType:      checks.CheckTypeMetric,
				checks.DataKeyScope:     checks.MetricScopeService,
				checks.DataKeyMetric:    "memory",
				checks.DataKeyOp:        ">",
				checks.DataKeyThreshold: "174159463",
				checks.DataKeyValue:     2555904,
				checks.DataKeyUnit:      metrics.MetricUnitBytes,
			},
		},
	}}
	tree := map[string]any{"rules": map[string]any{
		"memory-high": map[string]any{
			"type": "alert",
			"if":   map[string]any{"active": map[string]any{"check": "memory-high"}},
			"then": map[string]any{"action": "alert", "message": "${check.type}/${check.metric} ${check.op} ${check.threshold}: ${check.value}"},
		},
	}}
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
	w.RunCycle(context.Background())

	e, ok := h.eventOf(eventKindAlert)
	if !ok {
		t.Fatalf("no alert emitted: %+v", h.events)
	}
	if want := "metric/memory > 166,09 MB: 2,44 MB"; e.Message != want {
		t.Fatalf("message = %q, want %q", e.Message, want)
	}
}

func TestCycleAlertRecoveryCarriesMetricContext(t *testing.T) {
	h := &workerHarness{cache: map[string]checks.Result{
		"cpu-thread-high": {
			Check: "cpu-thread-high",
			OK:    false,
			Data: map[string]any{
				checks.DataKeyType:      checks.CheckTypeMetric,
				checks.DataKeyScope:     checks.MetricScopeService,
				checks.DataKeyMetric:    "cpu_thread",
				checks.DataKeyOp:        ">",
				checks.DataKeyThreshold: "90%",
				checks.DataKeyValue:     91.25,
				checks.DataKeyUnit:      metrics.MetricUnitPercent,
			},
		},
	}}
	tree := map[string]any{"rules": map[string]any{
		"alert-if-cpu-thread-high": map[string]any{
			"type": "alert",
			"if":   map[string]any{"failed": map[string]any{"check": "cpu-thread-high"}},
			"then": map[string]any{"action": "alert", "message": "CPU usage is high"},
		},
	}}
	w := h.worker(tree, rules.Policy{}, nil)
	w.RunCycle(context.Background())

	h.cache["cpu-thread-high"] = checks.Result{
		Check: "cpu-thread-high",
		OK:    true,
		Data: map[string]any{
			checks.DataKeyType:      checks.CheckTypeMetric,
			checks.DataKeyScope:     checks.MetricScopeService,
			checks.DataKeyMetric:    "cpu_thread",
			checks.DataKeyOp:        ">",
			checks.DataKeyThreshold: "90%",
			checks.DataKeyValue:     0.1998902696035153,
			checks.DataKeyUnit:      metrics.MetricUnitPercent,
		},
	}
	w.RunCycle(context.Background())

	e, ok := h.eventOf(eventKindRecovered)
	if !ok {
		t.Fatalf("no recovered event emitted: %+v", h.events)
	}
	if want := "rule condition recovered: metric cpu_thread current 0,2% (threshold > 90%)"; e.Message != want {
		t.Fatalf("recovered message = %q, want %q", e.Message, want)
	}
}

func TestCycleAlertRecoveryFormatsByteMetricContext(t *testing.T) {
	h := &workerHarness{cache: map[string]checks.Result{
		"memory-high": {
			Check: "memory-high",
			OK:    false,
			Data: map[string]any{
				checks.DataKeyType:      checks.CheckTypeMetric,
				checks.DataKeyScope:     checks.MetricScopeService,
				checks.DataKeyMetric:    "memory",
				checks.DataKeyOp:        ">",
				checks.DataKeyThreshold: "174159463",
				checks.DataKeyValue:     2555904,
				checks.DataKeyUnit:      metrics.MetricUnitBytes,
			},
		},
	}}
	tree := map[string]any{"rules": map[string]any{
		"alert-if-memory-high": map[string]any{
			"type": "alert",
			"if":   map[string]any{"failed": map[string]any{"check": "memory-high"}},
			"then": map[string]any{"action": "alert", "message": "memory is high"},
		},
	}}
	w := h.worker(tree, rules.Policy{}, nil)
	w.RunCycle(context.Background())

	h.cache["memory-high"] = checks.Result{
		Check: "memory-high",
		OK:    true,
		Data: map[string]any{
			checks.DataKeyType:      checks.CheckTypeMetric,
			checks.DataKeyScope:     checks.MetricScopeService,
			checks.DataKeyMetric:    "memory",
			checks.DataKeyOp:        ">",
			checks.DataKeyThreshold: "174159463",
			checks.DataKeyValue:     2555904,
			checks.DataKeyUnit:      metrics.MetricUnitBytes,
		},
	}
	w.RunCycle(context.Background())

	e, ok := h.eventOf(eventKindRecovered)
	if !ok {
		t.Fatalf("no recovered event emitted: %+v", h.events)
	}
	if want := "rule condition recovered: metric memory current 2,44 MB (threshold > 166,09 MB)"; e.Message != want {
		t.Fatalf("recovered message = %q, want %q", e.Message, want)
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
	if e, ok := h.eventOf(eventKindSuppressed); !ok || e.Message != "cooldown" {
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
	if e, ok := h.eventOf(eventKindSuppressed); !ok || e.Rule != "a-restart" {
		t.Fatalf("expected restart suppressed-by-cooldown: %+v", h.events)
	}
	if e, ok := h.eventOf(eventKindAlert); !ok || e.Rule != "b-notify" || e.Message != "http still down" {
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
	if len(h.ops) != 1 || h.ops[0] != string(rules.ActionStop) {
		t.Fatalf("ops = %v, want [stop] (restart blocked, first non-blocked wins)", h.ops)
	}
	if e, ok := h.eventOf(eventKindSuppressed); !ok || e.Action != string(rules.ActionRestart) {
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
	if e, ok := h.eventOf(eventKindAlert); !ok || e.Message != "http is down" {
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
	if len(h.ops) != 1 || h.ops[0] != string(rules.ActionRestart) {
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
	if len(h.ops) != 1 || h.ops[0] != string(rules.ActionRestart) {
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

	if len(h.ops) != 1 || h.ops[0] != string(rules.ActionRestart) {
		t.Fatalf("ops = %v, want [restart]", h.ops)
	}
	if e, ok := h.eventOf(eventKindAlert); !ok || e.Message != "http is down, restarting" {
		t.Fatalf("expected the alert action to also fire: %+v", h.events)
	}
	if _, ok := h.eventOf(eventKindAction); !ok {
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
	if _, ok := h.eventOf(eventKindAlert); ok {
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
	if w.fires(context.Background(), ev, r, t0, nil).firing {
		t.Fatal("a system-metric remediation rule must never fire")
	}
	if len(events) != 1 || events[0].Kind != eventKindError || !strings.Contains(events[0].Message, "alert rules") {
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
	if w.fires(context.Background(), ev, r, t0, nil).firing {
		t.Fatal("an inline system-metric remediation probe must never fire")
	}
	if len(events) != 1 || events[0].Kind != eventKindError || !strings.Contains(events[0].Message, "alert rules") {
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
	if w.fires(context.Background(), ev, r, t0, nil).firing {
		t.Fatal("a remediation rule referencing a system metric check must never fire")
	}
	if len(events) != 1 || events[0].Kind != eventKindError || !strings.Contains(events[0].Message, "alert rules") {
		t.Fatalf("check-ref events = %+v, want one error explaining the suppression", events)
	}

	// The same metric on an alert rule keeps working.
	r.Type = rules.RuleAlert
	r.If = map[string]any{"metric": map[string]any{"scope": "system", "name": "total_memory", "op": ">", "value": "90%"}}
	if !w.fires(context.Background(), ev, r, t0, nil).firing {
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

	if len(h.ops) != 1 || h.ops[0] != string(rules.ActionReload) {
		t.Fatalf("ops = %v, want one reload through the engine", h.ops)
	}
	if w.State.LastActionAt.IsZero() {
		t.Fatal("an executed reload must record remediation state (cooldown)")
	}
}

// TestWorkerDryRunEvaluatesButDoesNotAct verifies the core dry-run remediation
// behavior: conditions/windows/guards/policy are all evaluated and appropriate
// events are emitted, but no Operate is called and real RemediationState is not
// mutated.
func TestWorkerDryRunEvaluatesButDoesNotAct(t *testing.T) {
	h := &workerHarness{cache: failedCache("http")}
	tree := remediationTree("restart-if-down", "http", "restart")
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, nil)
	w.DryRun = true

	w.RunCycle(context.Background())

	if len(h.ops) != 0 {
		t.Fatalf("dry-run mode executed ops=%v; must not call Operate", h.ops)
	}
	ev, ok := h.eventOf(eventKindDryRun)
	if !ok {
		t.Fatalf("no dry-run event emitted; events=%+v", h.events)
	}
	if ev.Action != string(rules.ActionRestart) || !strings.Contains(ev.Message, "would") {
		t.Fatalf("dry-run event = %+v, want action=restart and 'would' in message", ev)
	}
	if !w.State.LastActionAt.IsZero() || len(w.State.RecentActions) != 0 {
		t.Error("dry-run must not Record remediation state (no pollution of real cooldown)")
	}
}

// TestWorkerDryRunReportsSuppression verifies that when a firing rule would be
// blocked (here by the cooldown policy), dry-run mode still emits a dry-run
// event whose message records why the action would have been suppressed, and
// that the seeded cooldown state is left untouched.
func TestWorkerDryRunReportsSuppression(t *testing.T) {
	h := &workerHarness{cache: failedCache("http")}
	state := &rules.RemediationState{LastActionAt: t0.Add(-30 * time.Second)} // within a 1m cooldown
	tree := remediationTree("restart-if-down", "http", "restart")
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, state)
	w.DryRun = true

	w.RunCycle(context.Background())

	if len(h.ops) != 0 {
		t.Fatalf("dry-run mode executed ops=%v; must not call Operate", h.ops)
	}
	if _, ok := h.eventOf(eventKindSuppressed); ok {
		t.Fatalf("dry-run mode must report via a dry-run event, not a plain suppressed one; events=%+v", h.events)
	}
	ev, ok := h.eventOf(eventKindDryRun)
	if !ok {
		t.Fatalf("no dry-run event emitted; events=%+v", h.events)
	}
	if ev.Action != string(rules.ActionRestart) || !strings.Contains(ev.Message, "suppressed: cooldown") {
		t.Fatalf("dry-run event = %+v, want action=restart and 'suppressed: cooldown' in message", ev)
	}
	if !w.State.LastActionAt.Equal(t0.Add(-30 * time.Second)) {
		t.Errorf("dry-run must not mutate the seeded cooldown state, LastActionAt=%v", w.State.LastActionAt)
	}
}

func TestWorkerDryRunHealthyCycleDoesNotRecoverBackoff(t *testing.T) {
	h := &workerHarness{cache: map[string]checks.Result{"http": {Check: "http", OK: true}}}
	state := &rules.RemediationState{CurrentBackoff: 5 * time.Minute}
	tree := remediationTree("restart-if-down", "http", "restart")
	w := h.worker(tree, rules.Policy{Cooldown: time.Minute}, state)
	w.DryRun = true

	w.RunCycle(context.Background())

	if w.State.CurrentBackoff != 5*time.Minute {
		t.Fatalf("dry-run healthy cycle mutated backoff = %s, want 5m", w.State.CurrentBackoff)
	}
}

func TestWorkerDryRunSendsOnlyWallAlerts(t *testing.T) {
	h := &workerHarness{cache: failedCache("http")}
	tree := alertRuleTree(nil)
	w := h.worker(tree, rules.Policy{}, nil)
	email := &fakeNotifier{name: "ops-email", typ: "email"}
	wall := &fakeNotifier{name: "wall", typ: "wall"}
	w.Notifiers = map[string]notify.Notifier{"ops-email": email, "wall": wall}
	w.GlobalNotify = []string{"ops-email", "wall"}
	w.DryRun = true

	w.RunCycle(context.Background())

	if len(email.msgs) != 0 {
		t.Fatalf("dry-run must suppress non-console notifications, got %d", len(email.msgs))
	}
	if len(wall.msgs) != 1 {
		t.Fatalf("dry-run must still send wall notification, got %d", len(wall.msgs))
	}
	if _, ok := h.eventOf(eventKindAlert); !ok {
		t.Fatalf("dry-run alert rule should still emit alert event: %+v", h.events)
	}
	if _, ok := h.eventOf(eventKindNotify); !ok {
		t.Fatalf("dry-run wall notification should emit notify event: %+v", h.events)
	}
}

func TestWorkerSettlesInactiveBackendOnObserveOnly(t *testing.T) {
	ready := NewReadiness(string(servicemgr.BackendSystemd), 1, 0)
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
	if rep := ready.Report(context.Background()); !rep.Ready || rep.Status != TargetStateOK {
		t.Fatalf("readiness = %+v, want ready after inactive observe-only cycle", rep)
	}
}

func TestSettlingDuplicateServiceAndAppNamesAdvanceReadiness(t *testing.T) {
	ready := NewReadiness(string(servicemgr.BackendSystemd), 2, 0)
	settling := NewSettling(ready)
	settling.Reset([]string{SettlingServiceKey("redis"), SettlingAppKey("redis")})
	ready.ExpectFirstCycles(2)

	settling.MarkObserved(SettlingServiceKey("redis"))
	settling.MarkObserved(SettlingAppKey("redis"))

	if !ready.Report(context.Background()).Ready {
		t.Fatal("service and app monitors with the same display name must both advance readiness")
	}
}
