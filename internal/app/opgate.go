package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"sermo/internal/config"
	"sermo/internal/locks"
	"sermo/internal/operation"
)

// OpGate serializes service operations across workers, the web UI and sermoctl
// using the global operation semaphore (section 24).
type OpGate struct {
	pool locks.SlotPool
	mem  chan struct{} // non-nil when runtimeDir was empty (tests)
}

// NewOpGate returns a gate with the given slot count. When runtimeDir is set,
// slots are shared across processes under <runtime>/op-slots; otherwise an
// in-memory semaphore is used (unit tests).
func NewOpGate(slots int, runtimeDir string) *OpGate {
	if slots <= 0 {
		slots = 2
	}
	if runtimeDir != "" {
		return &OpGate{pool: locks.NewSlotPool(filepath.Join(runtimeDir, "op-slots"), slots)}
	}
	return &OpGate{mem: make(chan struct{}, slots)}
}

// OpSlotsFromConfig reads engine.max_parallel_operations from the loaded config.
func OpSlotsFromConfig(cfg *config.Config) int {
	if cfg == nil {
		return 2
	}
	engine, ok := cfg.Global.Raw["engine"].(map[string]any)
	if !ok {
		return 2
	}
	switch v := engine["max_parallel_operations"].(type) {
	case int:
		if v > 0 {
			return v
		}
	case int64:
		if v > 0 {
			return int(v)
		}
	case float64:
		if v > 0 {
			return int(v)
		}
	}
	return 2
}

// Usage reports how many global operation slots are in use and the pool capacity.
func (g *OpGate) Usage() (inUse, total int) {
	if g == nil {
		return 0, 0
	}
	if g.mem != nil {
		return len(g.mem), cap(g.mem)
	}
	total = g.pool.Slots
	if total <= 0 {
		total = 2
	}
	n, err := g.pool.InUse()
	if err != nil {
		return 0, total
	}
	return n, total
}

// Run acquires a global operation slot, then invokes fn.
func (g *OpGate) Run(ctx context.Context, service, action string, fn func(context.Context) operation.Result) operation.Result {
	if g == nil {
		return fn(ctx)
	}
	release, ok := g.acquire(ctx)
	if !ok {
		return operation.Result{
			Service: service, Action: action, Status: operation.ResultFailed,
			Message: g.acquireFailureMessage(ctx),
		}
	}
	defer release()
	return fn(ctx)
}

// acquireFailureMessage explains why a global operation slot could not be taken
// before the caller's context ended.
func (g *OpGate) acquireFailureMessage(ctx context.Context) string {
	inUse, total := g.Usage()
	busy := total > 0 && inUse >= total
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		if busy {
			return fmt.Sprintf("operation slots busy (%d/%d); operation timeout exceeded", inUse, total)
		}
		return "operation timeout exceeded waiting for operation slot"
	case errors.Is(ctx.Err(), context.Canceled):
		return "shutting down"
	default:
		if busy {
			return fmt.Sprintf("operation slots busy (%d/%d)", inUse, total)
		}
		return "operation slot unavailable"
	}
}

func (g *OpGate) acquire(ctx context.Context) (func(), bool) {
	if g.mem != nil {
		select {
		case g.mem <- struct{}{}:
			return func() { <-g.mem }, true
		case <-ctx.Done():
			return nil, false
		}
	}
	h, err := g.pool.Acquire(ctx)
	if err != nil {
		return nil, false
	}
	return func() { _ = h.Release() }, true
}

// gateOperate wraps a worker's Operate so it waits for a global operation slot.
func gateOperate(w *Worker, g *OpGate) {
	inner := w.Operate
	w.Operate = func(ctx context.Context, action string) operation.Result {
		return g.Run(ctx, w.Service, action, func(ctx context.Context) operation.Result {
			return inner(ctx, action)
		})
	}
}
