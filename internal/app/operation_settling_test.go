package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/execx/execxtest"
	"sermo/internal/locks"
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
	if err := BeginOperationSettling(store, "db", string(rules.ActionStop)); err != nil {
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

	if err := BeginOperationSettling(store, "web", string(rules.ActionRestart)); err != nil {
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
	if err := finishOperationSettling(store, "web", string(rules.ActionRestart), result, nil, false); err != nil {
		t.Fatalf("finish restart: %v", err)
	}
	rec, found, err = store.OperationSettling("web")
	if err != nil || !found {
		t.Fatalf("operation settling after finish: found=%v err=%v", found, err)
	}
	if rec.Phase != state.OperationSettlingSettling {
		t.Fatalf("restart should wait for observation, got %+v", rec)
	}

	if err := BeginOperationSettling(store, "web", string(rules.ActionStop)); err != nil {
		t.Fatalf("begin stop: %v", err)
	}
	result = operation.Result{Service: "web", Action: string(rules.ActionStop), Status: operation.ResultOK}
	if err := finishOperationSettling(store, "web", string(rules.ActionStop), result, nil, false); err != nil {
		t.Fatalf("finish stop: %v", err)
	}
	if _, found, _ = store.OperationSettling("web"); found {
		t.Fatal("successful stop should clear operation settling")
	}

	if err := BeginOperationSettling(store, "web", string(rules.ActionStart)); err != nil {
		t.Fatalf("begin failed start: %v", err)
	}
	result = operation.Result{Service: "web", Action: string(rules.ActionStart), Status: operation.ResultFailed}
	if err := finishOperationSettling(store, "web", string(rules.ActionStart), result, errors.New("failed"), false); err != nil {
		t.Fatalf("finish failed start: %v", err)
	}
	if _, found, _ = store.OperationSettling("web"); found {
		t.Fatal("failed operation should clear operation settling")
	}

	if err := BeginOperationSettling(store, "web", string(rules.ActionRestart)); err != nil {
		t.Fatalf("begin active postflight restart: %v", err)
	}
	result = operation.Result{Service: "web", Action: string(rules.ActionRestart), Status: operation.ResultPostflightFailed}
	if err := finishOperationSettling(store, "web", string(rules.ActionRestart), result, nil, true); err != nil {
		t.Fatalf("finish active postflight restart: %v", err)
	}
	rec, found, err = store.OperationSettling("web")
	if err != nil || !found {
		t.Fatalf("active postflight restart should keep settling: found=%v err=%v", found, err)
	}
	if rec.Phase != state.OperationSettlingSettling {
		t.Fatalf("active postflight restart record = %+v", rec)
	}

	if err := finishOperationSettling(store, "web", string(rules.ActionRestart), result, nil, false); err != nil {
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
			if err := BeginOperationSettling(store, "db", string(rules.ActionStop)); err != nil {
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

	if err := BeginOperationSettling(store, "web", operation.ActionRepair); err != nil {
		t.Fatalf("begin repair: %v", err)
	}
	rec, found, err := store.OperationSettling("web")
	if err != nil || !found || rec.Phase != state.OperationSettlingRunning {
		t.Fatalf("running repair settling = %+v found=%v err=%v", rec, found, err)
	}
	if err := finishOperationSettling(store, "web", operation.ActionRepair, result, nil, false); err != nil {
		t.Fatalf("finish repair: %v", err)
	}
	rec, found, err = store.OperationSettling("web")
	if err != nil || !found || rec.Phase != state.OperationSettlingSettling {
		t.Fatalf("settled repair = %+v found=%v err=%v", rec, found, err)
	}
}
