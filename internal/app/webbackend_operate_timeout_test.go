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
// test can pin that Operate is bounded by the service's effective timeout.
type hangingManager struct{ fakeManager }

func (hangingManager) Start(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

// delayedStartManager completes only after delay unless the operation context
// expires first. It lets the test distinguish the service's resolved timeout
// from the daemon-wide configured value.
type delayedStartManager struct {
	fakeManager
	delay time.Duration
}

func (m delayedStartManager) Start(ctx context.Context, _ string) error {
	select {
	case <-time.After(m.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (delayedStartManager) Status(context.Context, string) (servicemgr.ServiceStatus, error) {
	return servicemgr.ServiceStatus{Status: servicemgr.StatusActive}, nil
}

// TestWebBackendOperateBoundsBackendHang pins that a web Operate cannot hang on
// a stuck backend operation: the handler bounds the whole call by the resolved
// per-service timeout and returns a non-OK result. Before the fix the handler
// used an unbounded context, so the goroutine could block until daemon shutdown.
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
	engine.OperationTimeout = 100 * time.Millisecond

	b := &WebBackend{
		entries:          map[string]*webEntry{"web": {engine: engine}},
		operationTimeout: 10 * time.Millisecond,
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
		if elapsed := time.Since(start); elapsed < 50*time.Millisecond || elapsed > 7*time.Second {
			t.Fatalf("operate took %v, want the service timeout", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Operate hung on a stuck backend (unbounded context)")
	}
}

func TestWebBackendOperateUsesEffectiveServiceTimeout(t *testing.T) {
	dir := t.TempDir()
	locker := locks.NewOperationLocker(locks.RuntimeOpsDir(dir))
	engine := operation.New(operation.Config{
		Service: "web",
		Unit:    "nginx",
		Backend: string(servicemgr.BackendSystemd),
		Tree:    map[string]any{"policy": map[string]any{"cooldown": "5m"}},
		Manager: delayedStartManager{delay: 40 * time.Millisecond},
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
	engine.OperationTimeout = 6 * time.Second
	b := &WebBackend{
		entries:          map[string]*webEntry{"web": {engine: engine}},
		operationTimeout: 10 * time.Millisecond,
		emit:             func(Event) {},
	}

	started := time.Now()
	if result := b.Operate(context.Background(), "web", "start", web.OperateOpts{}); !result.OK {
		t.Fatalf("operate = %+v, want successful start with service timeout", result)
	}
	if elapsed := time.Since(started); elapsed < 4*time.Second || elapsed > 6*time.Second {
		t.Fatalf("operate took %v, want the bounded postflight window within the service timeout", elapsed)
	}
}
