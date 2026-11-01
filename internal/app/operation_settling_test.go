package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx/execxtest"
	"sermo/internal/locks"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
)

func TestOperationSettlingOutlivesConfiguredTimeoutWhileLockHeld(t *testing.T) {
	useFakeLockProber(t)
	now := time.Now()
	store := newFakeStore()
	store.now = func() time.Time { return now.Add(-20 * time.Minute) }
	if _, err := BeginOperationSettling(store, "db", string(rules.ActionStop)); err != nil {
		t.Fatal(err)
	}
	runtime := t.TempDir()
	writeWebLockFixture(t, locks.RuntimeOpsDir(runtime), "db.lock", map[string]any{
		"service": "db", "owner_pid": os.Getpid(), "owner_start_ticks": fakeOwnerTicks,
		"expires_at": now.Add(10 * time.Minute).Format(time.RFC3339),
	})
	w, _, warnings := buildWorker(t.Context(), "db", "db.service", map[string]any{}, Deps{
		Backend: servicemgr.BackendSystemd, Manager: fakeManager{}, Runtime: runtime,
		OperationTimeout: operation.DefaultOperationTimeout, OperationSettling: store,
		ExecxRunner: &execxtest.Runner{}, ProcReader: serviceOwnershipReader{},
	}, nil)
	if len(warnings) != 0 {
		t.Fatalf("buildWorker warnings: %v", warnings)
	}
	if _, running := w.operationSettlingState(now); !running {
		t.Fatal("extended systemd operation lost its running marker while its lock is held")
	}
	if err := os.Remove(filepath.Join(locks.RuntimeOpsDir(runtime), "db.lock")); err != nil {
		t.Fatal(err)
	}
	if _, running := w.operationSettlingState(now); running {
		t.Fatal("abandoned marker stayed running after its lock disappeared")
	}
	if _, found, err := store.OperationSettling("db"); err != nil || found {
		t.Fatalf("abandoned marker: found=%v err=%v", found, err)
	}
}

func TestOperationSettlingLifecycle(t *testing.T) {
	store := newFakeStore()

	if _, err := BeginOperationSettling(store, "web", string(rules.ActionRestart)); err != nil {
		t.Fatalf("begin restart: %v", err)
	}
	rec, found, err := store.OperationSettling("web")
	if err != nil || !found {
		t.Fatalf("operation settling after begin: found=%v err=%v", found, err)
	}
	if rec.Phase != state.OperationSettlingRunning {
		t.Fatalf("begin record = %+v", rec)
	}

	result := operation.Result{Service: "web", Action: string(rules.ActionRestart), Status: operation.ResultOK}
	if err := finishOperationSettling(store, "web", string(rules.ActionRestart), result, nil, false, "", state.OperationSettlingSettling); err != nil {
		t.Fatalf("finish restart: %v", err)
	}
	rec, found, err = store.OperationSettling("web")
	if err != nil || !found {
		t.Fatalf("operation settling after finish: found=%v err=%v", found, err)
	}
	if rec.Phase != state.OperationSettlingSettling {
		t.Fatalf("restart should wait for observation, got %+v", rec)
	}

	if _, err := BeginOperationSettling(store, "web", string(rules.ActionStop)); err != nil {
		t.Fatalf("begin stop: %v", err)
	}
	result = operation.Result{Service: "web", Action: string(rules.ActionStop), Status: operation.ResultOK}
	if err := finishOperationSettling(store, "web", string(rules.ActionStop), result, nil, false, "", state.OperationSettlingSettling); err != nil {
		t.Fatalf("finish stop: %v", err)
	}
	if _, found, _ = store.OperationSettling("web"); found {
		t.Fatal("successful stop should clear operation settling")
	}

	if _, err := BeginOperationSettling(store, "web", string(rules.ActionStart)); err != nil {
		t.Fatalf("begin failed start: %v", err)
	}
	result = operation.Result{Service: "web", Action: string(rules.ActionStart), Status: operation.ResultFailed}
	if err := finishOperationSettling(store, "web", string(rules.ActionStart), result, errors.New("failed"), false, "", state.OperationSettlingSettling); err != nil {
		t.Fatalf("finish failed start: %v", err)
	}
	if _, found, _ = store.OperationSettling("web"); found {
		t.Fatal("failed operation should clear operation settling")
	}

	if _, err := BeginOperationSettling(store, "web", string(rules.ActionRestart)); err != nil {
		t.Fatalf("begin active postflight restart: %v", err)
	}
	result = operation.Result{Service: "web", Action: string(rules.ActionRestart), Status: operation.ResultPostflightFailed}
	if err := finishOperationSettling(store, "web", string(rules.ActionRestart), result, nil, true, "", state.OperationSettlingSettling); err != nil {
		t.Fatalf("finish active postflight restart: %v", err)
	}
	rec, found, err = store.OperationSettling("web")
	if err != nil || !found {
		t.Fatalf("active postflight restart should keep settling: found=%v err=%v", found, err)
	}
	if rec.Phase != state.OperationSettlingSettling {
		t.Fatalf("active postflight restart record = %+v", rec)
	}

	if err := finishOperationSettling(store, "web", string(rules.ActionRestart), result, nil, false, "", state.OperationSettlingSettling); err != nil {
		t.Fatalf("finish inactive postflight restart: %v", err)
	}
	if _, found, _ = store.OperationSettling("web"); found {
		t.Fatal("inactive postflight restart should clear operation settling")
	}
}

// TestOperationSettlingMarkerOutlivesLongOperations: a "running" marker only
// expires as abandoned once it is older than the service's own resolved
// operation deadline (stop_policy can raise it past the 15-minute floor), so a
// slow deliberate stop keeps suppressing rules, alerts and SLA until it ends.
func TestOperationSettlingMarkerOutlivesLongOperations(t *testing.T) {
	t0 := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		timeout     time.Duration
		age         time.Duration
		wantRunning bool
	}{
		{name: "long stop still running", timeout: 30 * time.Minute, age: 20 * time.Minute, wantRunning: true},
		{name: "long stop abandoned", timeout: 30 * time.Minute, age: 40 * time.Minute, wantRunning: false},
		{name: "short timeout keeps the floor", timeout: time.Minute, age: 10 * time.Minute, wantRunning: true},
		{name: "short timeout abandoned after the floor", timeout: time.Minute, age: 20 * time.Minute, wantRunning: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			store.now = func() time.Time { return t0 }
			if _, err := BeginOperationSettling(store, "db", string(rules.ActionStop)); err != nil {
				t.Fatal(err)
			}
			w := &Worker{Service: "db", OperationSettling: store, OperationTimeout: tt.timeout}
			if _, running := w.operationSettlingState(t0.Add(tt.age)); running != tt.wantRunning {
				t.Fatalf("running = %t, want %t", running, tt.wantRunning)
			}
		})
	}
}

func TestRepairOperationSettlesLikeStart(t *testing.T) {
	store := newFakeStore()
	result := operation.Result{Service: "web", Action: operation.ActionRepair, Status: operation.ResultOK}

	if _, err := BeginOperationSettling(store, "web", operation.ActionRepair); err != nil {
		t.Fatalf("begin repair: %v", err)
	}
	rec, found, err := store.OperationSettling("web")
	if err != nil || !found || rec.Phase != state.OperationSettlingRunning {
		t.Fatalf("running repair settling = %+v found=%v err=%v", rec, found, err)
	}
	if err := finishOperationSettling(store, "web", operation.ActionRepair, result, nil, false, "", state.OperationSettlingSettling); err != nil {
		t.Fatalf("finish repair: %v", err)
	}
	rec, found, err = store.OperationSettling("web")
	if err != nil || !found || rec.Phase != state.OperationSettlingSettling {
		t.Fatalf("settled repair = %+v found=%v err=%v", rec, found, err)
	}
}

// A second attempt the operation lock refuses never ran. Whatever it wrote, the
// running marker belongs to the operation that holds the lock, and clearing it
// would resume monitoring in the middle of that operation.
func TestBlockedAttemptKeepsTheRunningOperationsMarker(t *testing.T) {
	store := newFakeStore()
	if err := store.SetOperationSettling("target", state.OperationSettlingRunning); err != nil {
		t.Fatal(err)
	}
	target := &Worker{Service: "target", OperationSettling: store, Operate: func(_ context.Context, action string) operation.Result {
		return operation.Result{Action: action, Status: operation.ResultBlocked}
	}}

	target.operateForRemediation(t.Context(), string(rules.ActionRestart))

	if rec, found, err := store.OperationSettling("target"); err != nil || !found || rec.Phase != state.OperationSettlingRunning {
		t.Fatalf("marker = %+v found=%v err=%v, want the running operation's", rec, found, err)
	}
}

// The web UI and sermoctl finish through CompleteManualOperation.
func TestManualBlockedAttemptKeepsTheRunningOperationsMarker(t *testing.T) {
	store := newFakeStore()
	if _, err := BeginOperationSettling(store, "web", string(rules.ActionRestart)); err != nil {
		t.Fatal(err)
	}
	prior, err := BeginOperationSettling(store, "web", string(rules.ActionRestart))
	if err != nil || prior != state.OperationSettlingRunning {
		t.Fatalf("second attempt: prior=%q err=%v", prior, err)
	}
	blocked := operation.Result{Service: "web", Action: string(rules.ActionRestart), Status: operation.ResultBlocked}

	if _, err := CompleteManualOperation(store, store, "web", string(rules.ActionRestart), blocked, nil, ManualOperationSources{}, false, prior); err != nil {
		t.Fatal(err)
	}

	if rec, found, err := store.OperationSettling("web"); err != nil || !found || rec.Phase != state.OperationSettlingRunning {
		t.Fatalf("marker = %+v found=%v err=%v, want the running operation's", rec, found, err)
	}
}

// A marker a blocked attempt wrote itself is its own to remove: what held the
// lock may keep none, as a statement kill does, and nothing else would clear it.
func TestBlockedAttemptRemovesItsOwnMarker(t *testing.T) {
	store := newFakeStore()
	w := &Worker{Service: "web", OperationSettling: store, Operate: func(_ context.Context, action string) operation.Result {
		return operation.Result{Action: action, Status: operation.ResultBlocked}
	}}

	w.operateForRemediation(t.Context(), string(rules.ActionRestart))

	if rec, found, err := store.OperationSettling("web"); err != nil || found {
		t.Fatalf("marker = %+v found=%v err=%v, want none", rec, found, err)
	}
}

// A marker's caller may have died with it. If each refused attempt renewed its
// time, attempts coming faster than the age limit would keep the service's
// monitoring suspended for good.
func TestBlockedAttemptDoesNotRenewAnAbandonedMarker(t *testing.T) {
	now := time.Now()
	store := newFakeStore()
	store.now = func() time.Time { return now.Add(-20 * time.Minute) }
	if err := store.SetOperationSettling("web", state.OperationSettlingRunning); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now }
	w := &Worker{Service: "web", OperationSettling: store, Operate: func(_ context.Context, action string) operation.Result {
		return operation.Result{Action: action, Status: operation.ResultBlocked}
	}}

	w.operateForRemediation(t.Context(), string(rules.ActionRestart))

	if _, running := w.operationSettlingState(now); running {
		t.Fatal("a refused attempt kept an abandoned marker alive")
	}
}

// An attempt that never ran owes nothing to the operation before it, whose
// observation cycle is still due: a live cycle would read checks from before it.
// One that ran and failed ends that operation's story.
func TestLaterAttemptOverAPendingObservation(t *testing.T) {
	for name, tc := range map[string]struct {
		status operation.ResultStatus
		kept   bool
	}{
		"blocked": {status: operation.ResultBlocked, kept: true},
		"failed":  {status: operation.ResultFailed},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			if err := store.SetOperationSettling("web", state.OperationSettlingSettling); err != nil {
				t.Fatal(err)
			}
			action := string(rules.ActionReload)
			prior, err := BeginOperationSettling(store, "web", action)
			if err != nil {
				t.Fatal(err)
			}
			result := operation.Result{Service: "web", Action: action, Status: tc.status}
			if _, err := CompleteManualOperation(store, store, "web", action, result, nil, ManualOperationSources{}, false, prior); err != nil {
				t.Fatal(err)
			}
			rec, found, err := store.OperationSettling("web")
			if err != nil || found != tc.kept || (tc.kept && rec.Phase != state.OperationSettlingSettling) {
				t.Fatalf("marker = %+v found=%v err=%v, want kept=%t", rec, found, err, tc.kept)
			}
		})
	}
}

// An operator can start an operation while the worker observes the previous one:
// finishing that observation must not take the new operation's marker with it.
func TestObservationKeepsTheMarkerOfAnOperationStartedMeanwhile(t *testing.T) {
	for name, observed := range map[string]servicemgr.Status{"active": servicemgr.StatusActive, "inactive": servicemgr.StatusInactive} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			if err := store.SetOperationSettling("web", state.OperationSettlingSettling); err != nil {
				t.Fatal(err)
			}
			begin := func() {
				if err := store.SetOperationSettling("web", state.OperationSettlingRunning); err != nil {
					t.Fatal(err)
				}
			}
			h := &workerHarness{cache: map[string]checks.Result{}}
			w := h.worker(nil, rules.Policy{}, nil)
			w.OperationSettling = store
			w.CheckDeps.Status = func(context.Context) (servicemgr.Status, error) {
				begin()
				return observed, nil
			}

			w.RunCycle(t.Context())

			if rec, found, err := store.OperationSettling("web"); err != nil || !found || rec.Phase != state.OperationSettlingRunning {
				t.Fatalf("marker = %+v found=%v err=%v, want the new operation's", rec, found, err)
			}
		})
	}
}

// manualRestartFixture is a service with a skip_when_changed check and a
// changed: restart rule, whose file changed before an operator acts on it.
func manualRestartFixture(t *testing.T) (w *Worker, h *workerHarness, store *fakeStore, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "app.conf")
	writeFile(t, path, "a")
	store = newFakeStore()
	h = &workerHarness{cache: map[string]checks.Result{"probe": {Check: "probe", OK: false}}, opResult: operation.Result{Status: operation.ResultOK}}
	w = h.worker(map[string]any{"rules": map[string]any{"restart-on-change-config-1": changedRule(path, "restart")}}, rules.Policy{Cooldown: time.Minute}, nil)
	w.OperationSettling = store
	w.Gates = map[string]CheckGate{"probe": {SkipWhenChanged: []string{path}}}
	w.RunCycle(t.Context()) // adopts the baseline
	writeFile(t, path, "changed")
	return w, h, store, path
}

func completeManual(t *testing.T, store *fakeStore, action string, result operation.Result) {
	t.Helper()
	result.Service, result.Action = "web", action
	sources := ManualOperationSources{Stop: state.SourceCLIManualStop, Restore: state.SourceCLI}
	if _, err := CompleteManualOperation(store, store, "web", action, result, nil, sources, false, ""); err != nil {
		t.Fatal(err)
	}
}

// relaunched is what the engine reports for an action that launched the service.
var relaunched = operation.Result{Status: operation.ResultOK, Relaunched: true}

// An operator's restart takes in what changed on disk, so the worker must adopt
// it: otherwise the changed: rule restarts the service a second time and a
// skip_when_changed check stays skipped for as long as sermod runs. It is an
// operator action, so a dry_run worker adopts it too, and it reaches sermod from
// another process through the marker alone.
func TestManualRestartAcknowledgesTheChangedBaseline(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		for _, action := range []string{string(rules.ActionRestart), string(rules.ActionStart), operation.ActionRepair} {
			t.Run(fmt.Sprintf("%s dry_run=%t", action, dryRun), func(t *testing.T) {
				w, h, store, path := manualRestartFixture(t)
				w.DryRun = dryRun

				completeManual(t, store, action, relaunched)
				w.RunCycle(t.Context()) // the observation cycle

				if changed, err := w.changed(path); err != nil || changed {
					t.Fatalf("changed = %t, %v after the operator's %s", changed, err, action)
				}
				w.RunCycle(t.Context())
				if reason := w.gateReason(w.Gates["probe"], nil); len(h.ops) != 0 || reason != "" {
					t.Fatalf("ops = %v gate = %q, want no second restart and the check resumed", h.ops, reason)
				}
				if _, found, _ := store.OperationSettling("web"); found {
					t.Fatal("the marker must be consumed")
				}
			})
		}
	}
}

func TestManualOperationThatDidNotRelaunchKeepsTheChangePending(t *testing.T) {
	tests := []struct {
		name   string
		action string
		status operation.ResultStatus
	}{
		{name: "reload", action: string(rules.ActionReload), status: operation.ResultOK},
		{name: "resume", action: string(rules.ActionResume), status: operation.ResultOK},
		{name: "failed restart", action: string(rules.ActionRestart), status: operation.ResultFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, _, store, path := manualRestartFixture(t)

			completeManual(t, store, tt.action, operation.Result{Status: tt.status})
			w.Rules = nil // only the baseline is under test here
			w.RunCycle(t.Context())

			if changed, err := w.changed(path); err != nil || !changed {
				t.Fatalf("changed = %t, %v, want the change pending", changed, err)
			}
		})
	}
}

// A start on a service already running changes nothing, so an operator's start
// must not adopt a change only a restart brings in: the rule stays pending.
func TestManualStartOnARunningServiceKeepsTheChangePending(t *testing.T) {
	w, h, store, path := manualRestartFixture(t)

	completeManual(t, store, string(rules.ActionStart), operation.Result{Status: operation.ResultOK})
	w.RunCycle(t.Context()) // the observation cycle

	if changed, err := w.changed(path); err != nil || !changed {
		t.Fatalf("changed = %t, %v, want the change pending after a start that relaunched nothing", changed, err)
	}
	w.RunCycle(t.Context())
	if !eq(h.ops, []string{"restart"}) {
		t.Fatalf("ops = %v, want the restart the change is waiting for", h.ops)
	}
}

// An operator's restart stays owed to the worker until it observes it, whatever
// the operator does next inside one interval: forgetting it would restart the
// service again for a change it already took in.
func TestLaterAttemptKeepsAnUnobservedRelaunch(t *testing.T) {
	blockedByGuard := operation.Result{Status: operation.ResultBlocked}
	failed := operation.Result{Status: operation.ResultFailed}
	ok := operation.Result{Status: operation.ResultOK}
	tests := []struct {
		name   string
		action string
		result operation.Result
	}{
		{name: "reload", action: string(rules.ActionReload), result: ok},
		{name: "start on the running service", action: string(rules.ActionStart), result: ok},
		{name: "failed reload", action: string(rules.ActionReload), result: failed},
		{name: "blocked reload", action: string(rules.ActionReload), result: blockedByGuard},
		{name: "stop", action: string(rules.ActionStop), result: ok},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, h, store, path := manualRestartFixture(t)
			completeManual(t, store, string(rules.ActionRestart), relaunched)

			prior, err := BeginOperationSettling(store, "web", tt.action)
			if err != nil {
				t.Fatal(err)
			}
			tt.result.Service, tt.result.Action = "web", tt.action
			if _, err := CompleteManualOperation(store, store, "web", tt.action, tt.result, nil, ManualOperationSources{}, false, prior); err != nil {
				t.Fatal(err)
			}
			w.RunCycle(t.Context()) // the observation cycle
			w.RunCycle(t.Context())

			if changed, err := w.changed(path); err != nil || changed || len(h.ops) != 0 {
				t.Fatalf("changed = %t, %v ops = %v, want the operator's restart to count", changed, err, h.ops)
			}
		})
	}
}

// The first relaunch still counts when a second one is the operation running.
func TestBlockedAttemptLeavesARunningOperationOverARelaunch(t *testing.T) {
	store := newFakeStore()
	completeManual(t, store, string(rules.ActionRestart), relaunched)
	first, err := BeginOperationSettling(store, "web", string(rules.ActionRestart))
	if err != nil {
		t.Fatal(err)
	}
	second, err := BeginOperationSettling(store, "web", string(rules.ActionReload))
	if err != nil {
		t.Fatal(err)
	}
	blocked := operation.Result{Service: "web", Action: string(rules.ActionReload), Status: operation.ResultBlocked}
	if _, err := CompleteManualOperation(store, store, "web", string(rules.ActionReload), blocked, nil, ManualOperationSources{}, false, second); err != nil {
		t.Fatal(err)
	}
	if rec, found, err := store.OperationSettling("web"); err != nil || !found || rec.Phase != state.OperationSettlingRunning {
		t.Fatalf("marker = %+v found=%v err=%v, want the running operation's", rec, found, err)
	}
	failed := operation.Result{Service: "web", Action: string(rules.ActionRestart), Status: operation.ResultFailed}
	if _, err := CompleteManualOperation(store, store, "web", string(rules.ActionRestart), failed, nil, ManualOperationSources{}, false, first); err != nil {
		t.Fatal(err)
	}
	if rec, found, err := store.OperationSettling("web"); err != nil || !found || rec.Phase != state.OperationSettlingRelaunched {
		t.Fatalf("marker = %+v found=%v err=%v, want the first relaunch still owed", rec, found, err)
	}
}

// Maintenance under an explicit unmonitor: the restart happened, and a pause must
// not keep the worker from learning it, however long the pause lasts. The marker
// also says the new process is Sermo's own, so the first live cycle after a short
// pause must still not announce it as an external restart.
func TestManualRestartIsAcknowledgedDuringAPause(t *testing.T) {
	for name, age := range map[string]time.Duration{"short pause": 0, "long pause": time.Hour} {
		t.Run(name, func(t *testing.T) {
			w, h, store, _ := manualRestartFixture(t)
			paused := true
			w.IsPaused = func() bool { return paused }
			store.now = func() time.Time { return t0.Add(-age) }
			n := &fakeNotifier{name: "ops"}
			w.RestartNotice, w.ServiceRestartNotice, w.PrimaryProcess = restartNoticeConfig(), store, recentPrimaryProcess
			w.Notifiers = map[string]notify.Notifier{"ops": n}

			completeManual(t, store, string(rules.ActionRestart), relaunched)
			w.RunCycle(t.Context())
			if rec, found, err := store.OperationSettling("web"); age == 0 && (err != nil || !found || rec.Phase != state.OperationSettlingSettling) {
				t.Fatalf("marker = %+v found=%v err=%v, want it kept as settling", rec, found, err)
			}
			paused = false
			w.RunCycle(t.Context())

			if len(h.ops) != 0 {
				t.Fatalf("ops = %v, want the operator's restart to count", h.ops)
			}
			if age == 0 && len(n.msgs) != 0 {
				t.Fatalf("Sermo's own restart produced %d external-restart notifications", len(n.msgs))
			}
		})
	}
}

// The worker must not rewrite a marker a new operation replaced after it read it.
func TestPausedRelaunchLeavesAMarkerThatMovedOn(t *testing.T) {
	store := newFakeStore()
	if err := store.SetOperationSettling("web", state.OperationSettlingRunning); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceOperationSettlingPhase("web", state.OperationSettlingRelaunched, state.OperationSettlingSettling); err != nil {
		t.Fatal(err)
	}
	if rec, found, err := store.OperationSettling("web"); err != nil || !found || rec.Phase != state.OperationSettlingRunning {
		t.Fatalf("marker = %+v found=%v err=%v, want the running operation's", rec, found, err)
	}
}

// A worker on a long interval may first see the marker past the age limit. A
// finished relaunch is no abandoned operation: it still counts.
func TestManualRestartAcknowledgedPastTheAgeLimit(t *testing.T) {
	w, h, store, _ := manualRestartFixture(t)
	store.now = func() time.Time { return t0.Add(-24 * time.Hour) }

	completeManual(t, store, string(rules.ActionRestart), relaunched)
	w.RunCycle(t.Context())
	w.RunCycle(t.Context())

	if len(h.ops) != 0 {
		t.Fatalf("ops = %v, want the operator's restart to count", h.ops)
	}
}
