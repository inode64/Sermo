package app

import (
	"context"
	"sync"
	"time"

	"sermo/internal/operation"
)

// Scheduler runs each worker on its own goroutine with an independent interval
// timer measured from cycle completion (so overruns skip ticks, never queue
// them), spreads worker starts with jitter, and bounds concurrent operations
// across all services with a global semaphore (section 24).
type Scheduler struct {
	Interval time.Duration
	OpSlots  int // global operation semaphore; <=0 means a default of 2
	// StartupDelay holds the daemon for this long before starting any worker,
	// giving the host time to finish booting so services that are still coming
	// up are not flagged or remediated prematurely. <=0 disables the wait.
	StartupDelay time.Duration
}

// cycler is anything the scheduler ticks once per interval.
type cycler interface {
	RunCycle(ctx context.Context)
}

// Run starts every worker and watch and blocks until ctx is cancelled and all of
// them have returned (graceful shutdown, section 24). Each worker's Operate is
// wrapped so it waits for a global operation slot, pausing only that service's
// monitoring. Watches run on their own goroutines using their own interval.
func (s Scheduler) Run(ctx context.Context, workers []*Worker, watches []*Watch) {
	slots := s.OpSlots
	if slots <= 0 {
		slots = 2
	}
	sem := make(chan struct{}, slots)

	interval := s.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	// Grace period before the first cycle so a still-booting host can settle.
	// A shutdown signal during the wait aborts cleanly without starting workers.
	if s.StartupDelay > 0 {
		if !sleepCtx(ctx, s.StartupDelay) {
			return
		}
	}

	var wg sync.WaitGroup
	for i, w := range workers {
		gateOperate(w, sem)
		offset := time.Duration(int64(interval) * int64(i) / int64(len(workers)))
		wg.Add(1)
		go func(w *Worker, offset time.Duration) {
			defer wg.Done()
			runCycler(ctx, w, interval, offset)
		}(w, offset)
	}
	for _, wt := range watches {
		wi := wt.Interval
		if wi <= 0 {
			wi = interval
		}
		wg.Add(1)
		go func(wt *Watch, wi time.Duration) {
			defer wg.Done()
			runCycler(ctx, wt, wi, 0)
		}(wt, wi)
	}
	wg.Wait()
}

// runCycler ticks a cycler from cycle completion: jitter, then cycle, then wait
// one interval, repeat. A cancelled context stops between cycles (section 24:
// never start a new operation during shutdown).
func runCycler(ctx context.Context, c cycler, interval, offset time.Duration) {
	if offset > 0 {
		if !sleepCtx(ctx, offset) {
			return
		}
	}
	for {
		if ctx.Err() != nil {
			return
		}
		c.RunCycle(ctx)
		if !sleepCtx(ctx, interval) {
			return
		}
	}
}

// gateOperate wraps a worker's Operate so it acquires a global operation slot
// first, serializing mass restarts. While waiting, only this service's
// monitoring pauses.
func gateOperate(w *Worker, sem chan struct{}) {
	inner := w.Operate
	w.Operate = func(ctx context.Context, action string) operation.Result {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			return operation.Result{Service: w.Service, Action: action, Status: operation.ResultFailed, Message: "shutting down"}
		}
		return inner(ctx, action)
	}
}

// sleepCtx waits for d or ctx cancellation, returning false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
