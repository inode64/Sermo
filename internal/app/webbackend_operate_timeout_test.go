package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/locks"
	"sermo/internal/operation"
	"sermo/internal/servicemgr"
	"sermo/internal/web"
)

// TestWebBackendOperateBoundsSlotWait pins that a web Operate cannot hang waiting
// for a saturated operation-slot pool: it bounds the wait by operationTimeout and
// returns a non-OK result. Before the fix the handler used an unbounded context,
// so the goroutine could block until daemon shutdown.
func TestWebBackendOperateBoundsSlotWait(t *testing.T) {
	dir := t.TempDir()
	locker := locks.NewOperationLocker(locks.RuntimeOpsDir(dir))
	engine := operation.New(operation.Config{
		Service: "web",
		Unit:    "nginx",
		Backend: string(servicemgr.BackendSystemd),
		Tree:    map[string]any{"policy": map[string]any{"cooldown": "5m"}},
		Manager: fakeManager{},
		Locker:  &locker,
		Scanner: locks.NewScanner(locks.RuntimeLocksDir(dir)),
		CheckDeps: checks.Deps{
			DefaultTimeout: time.Second,
			Status: func(context.Context) (servicemgr.Status, error) {
				return servicemgr.StatusActive, nil
			},
		},
		Emit: operationEventEmitter(func(Event) {}),
	})

	gate := NewOpGate(1, "")
	gate.mem <- struct{}{} // saturate the only slot; it never frees

	b := &WebBackend{
		entries:          map[string]*webEntry{"web": {engine: engine}},
		opGate:           gate,
		operationTimeout: 100 * time.Millisecond,
		emit:             func(Event) {},
	}

	done := make(chan web.ActionResult, 1)
	start := time.Now()
	go func() { done <- b.Operate(context.Background(), "web", "start", web.OperateOpts{}) }()

	select {
	case res := <-done:
		if res.OK {
			t.Fatalf("operate should fail when no slot frees, got OK: %+v", res)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("operate took %v, want ~operationTimeout", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Operate hung waiting for a slot (unbounded context)")
	}
}
