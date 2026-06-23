package app

import (
	"context"
	"sync"
	"time"
)

// Scheduler runs each worker on its own goroutine with an independent interval
// timer measured from cycle completion (so overruns skip ticks, never queue
// them), spreads worker starts with jitter, and bounds concurrent operations
// across all services with a global semaphore.
type Scheduler struct {
	// Interval is the global default cycle interval (engine.interval) used by
	// every worker and watch that does not set its own. <=0 means 30s.
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
// them have returned (graceful shutdown). Each worker's Operate is
// wrapped so it waits for a global operation slot, pausing only that service's
// monitoring. Watches run on their own goroutines using their own interval.
// When finalShutdown is false (config reload), readiness is left unchanged.
func (s Scheduler) Run(ctx context.Context, workers []*Worker, watches []*Watch, opGate *OpGate, ready *Readiness, finalShutdown, gateReady bool) {
	if opGate == nil {
		opGate = NewOpGate(s.OpSlots, "")
	}

	interval := s.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}

	// Grace period before the first cycle so a still-booting host can settle.
	// A shutdown signal during the wait aborts cleanly without starting workers.
	if s.StartupDelay > 0 {
		if !sleepCtx(ctx, s.StartupDelay) {
			if ready != nil {
				ready.MarkShuttingDown()
			}
			return
		}
	}

	// On the first boot, hold the daemon at "starting" until every target has run
	// its first cycle (so it has data); each cycler reports via onFirstCycle. On a
	// config reload the daemon is already up, so mark it ready right away.
	total := len(workers) + len(watches)
	var onFirstCycle func()
	if gateReady {
		ready.ExpectFirstCycles(total)
		if ready != nil {
			onFirstCycle = ready.markFirstCycle
		}
	} else if ready != nil {
		ready.MarkReady()
	}

	// Stagger the first cycle of the whole fleet (workers + watches, including the
	// slow app-watches) across one general interval, ignoring each target's own
	// interval for that first cycle only. This avoids a startup stampede — every
	// app probe firing at once — while still checking everything within ~one
	// interval; runCycler then reverts each target to its own cadence.
	var wg sync.WaitGroup
	idx := 0
	for _, w := range workers {
		gateOperate(w, opGate)
		wi := w.Interval
		if wi <= 0 {
			wi = interval
		}
		offset := staggerOffset(idx, total, interval)
		idx++
		wg.Add(1)
		go func(w *Worker, wi, offset time.Duration) {
			defer wg.Done()
			runCycler(ctx, w, wi, offset, onFirstCycle)
		}(w, wi, offset)
	}
	for _, wt := range watches {
		wi := wt.Interval
		if wi <= 0 {
			wi = interval
		}
		offset := staggerOffset(idx, total, interval)
		idx++
		wg.Add(1)
		go func(wt *Watch, wi, offset time.Duration) {
			defer wg.Done()
			runCycler(ctx, wt, wi, offset, onFirstCycle)
		}(wt, wi, offset)
	}
	wg.Wait()
	if finalShutdown && ready != nil {
		ready.MarkShuttingDown()
	}
}

// staggerOffset spreads target idx of total evenly across one interval, so the
// whole fleet's first cycle is staggered instead of stampeding at startup. The
// first target starts immediately and the rest fan out up to (just under) one
// interval later.
func staggerOffset(idx, total int, interval time.Duration) time.Duration {
	if total <= 0 {
		return 0
	}
	return time.Duration(int64(interval) * int64(idx) / int64(total))
}

// runCycler ticks a cycler from cycle completion: jitter, then cycle, then wait
// one interval, repeat. A cancelled context stops between cycles (never start a
// new operation during shutdown). onFirstCycle, when set, fires once right after
// the first RunCycle returns — the readiness gate uses it to learn the target has
// data.
func runCycler(ctx context.Context, c cycler, interval, offset time.Duration, onFirstCycle func()) {
	if offset > 0 {
		if !sleepCtx(ctx, offset) {
			return
		}
	}
	first := true
	for {
		if ctx.Err() != nil {
			return
		}
		c.RunCycle(ctx)
		if first {
			first = false
			if onFirstCycle != nil {
				onFirstCycle()
			}
		}
		if !sleepCtx(ctx, interval) {
			return
		}
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
