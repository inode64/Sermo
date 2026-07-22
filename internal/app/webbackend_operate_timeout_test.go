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

// hangingManager blocks Start until the operation context is cancelled, so the
// test can pin that Operate is bounded by operationTimeout.
type hangingManager struct{ fakeManager }

func (hangingManager) Start(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

// TestWebBackendOperateBoundsBackendHang pins that a web Operate cannot hang on
// a stuck backend operation: the handler bounds the whole call by
// operationTimeout and returns a non-OK result. Before the fix the handler used
// an unbounded context, so the goroutine could block until daemon shutdown.
func TestWebBackendOperateBoundsBackendHang(t *testing.T) {
	dir := t.TempDir()
	locker := locks.NewOperationLocker(locks.RuntimeOpsDir(dir))
	engine := operation.New(operation.Config{
		Service: "web",
		Unit:    "nginx",
		Backend: string(servicemgr.BackendSystemd),
		Tree:    map[string]any{"policy": map[string]any{"cooldown": "5m"}},
		Manager: hangingManager{},
		Locker:  &locker,
		Scanner: locks.NewScanner(locks.RuntimeLocksDir(dir)),
		CheckDeps: checks.Deps{
			DefaultTimeout: time.Second,
			Status: func(context.Context) (servicemgr.Status, error) {
				return servicemgr.StatusInactive, nil
			},
		},
		Emit: operationEventEmitter(func(Event) {}),
	})

	b := &WebBackend{
		entries:          map[string]*webEntry{"web": {engine: engine}},
		operationTimeout: 100 * time.Millisecond,
		emit:             func(Event) {},
	}

	done := make(chan web.ActionResult, 1)
	start := time.Now()
	go func() { done <- b.Operate(context.Background(), "web", "start", web.OperateOpts{}) }()

	select {
	case res := <-done:
		if res.OK {
			t.Fatalf("operate should fail when the backend hangs, got OK: %+v", res)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("operate took %v, want ~operationTimeout", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Operate hung on a stuck backend (unbounded context)")
	}
}
