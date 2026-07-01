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

	// On the first boot, hold the daemon at "starting" until every active target
	// has completed its startup observation cycle (workers and watches call
	// Settling.MarkObserved when ready). Paused/disabled targets are excluded.
	// On a config reload the daemon is already up, so mark it ready right away.
	total := activeMonitorTargets(workers, watches)
	if gateReady {
		if ready != nil {
			ready.ExpectFirstCycles(total)
		}
	} else if ready != nil {
		ready.MarkReady()
	}

	// Stagger the first cycle of the whole fleet (workers + watches, including the
	// slow app-watches) across one general interval, ignoring each target's own
	// interval for that first cycle only. This avoids a startup stampede — every
	// app probe firing at once — while still checking everything within ~one
	// interval; runCycler then reverts each target to its own cadence.
	staggerTotal := len(workers) + len(watches)
	var wg sync.WaitGroup
	idx := 0
	for _, w := range workers {
		gateOperate(w, opGate)
		wi := w.Interval
		if wi <= 0 {
			wi = interval
		}
		offset := staggerOffset(idx, staggerTotal, interval)
		idx++
		wg.Add(1)
		go func(w *Worker, wi, offset time.Duration) {
			defer wg.Done()
			runCycler(ctx, w, wi, offset)
		}(w, wi, offset)
	}
	for _, wt := range watches {
		wi := wt.Interval
		if wi <= 0 {
			wi = interval
		}
		offset := staggerOffset(idx, staggerTotal, interval)
		idx++
		wg.Add(1)
		go func(wt *Watch, wi, offset time.Duration) {
			defer wg.Done()
			runCycler(ctx, wt, wi, offset)
		}(wt, wi, offset)
	}
	wg.Wait()
	if finalShutdown && ready != nil {
		ready.MarkShuttingDown()
	}
}

// activeMonitorTargets counts the distinct settling keys the first-cycle
// readiness gate must wait for. It must dedupe by key, not count objects:
// metric watches (net/icmp/swap) expand to one Watch per metric that all share
// a single settling key (SettlingWatchKey(name)), so a target reports observed
// only once. Counting objects here would arm the gate for more first cycles than
// can ever fire, wedging the daemon at "starting" (readyz 503) forever.
func activeMonitorTargets(workers []*Worker, watches []*Watch) int {
	keys := make(map[string]struct{})
	for _, w := range workers {
		if monitorTargetActive(w) {
			keys[SettlingServiceKey(w.Service)] = struct{}{}
		}
	}
	for _, wt := range watches {
		if watchTargetActive(wt) {
			keys[settlingKeyForWatch(wt)] = struct{}{}
		}
	}
	return len(keys)
}

func monitorTargetActive(w *Worker) bool {
	return w != nil && (w.IsPaused == nil || !w.IsPaused())
}

func watchTargetActive(wt *Watch) bool {
	return wt != nil && (wt.IsPaused == nil || !wt.IsPaused())
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
// new operation during shutdown).
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
