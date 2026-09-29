package app

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"sermo/internal/config"
	"sermo/internal/ctxutil"
)

// Scheduler runs each worker on its own goroutine with an independent interval
// timer measured from cycle completion (so overruns skip ticks, never queue
// them) and spreads worker starts with jitter. Operation concurrency is bounded
// per service by the operation lock and by mandatory remediation policy, not by
// a global cap.
type Scheduler struct {
	// Interval is the global default cycle interval (engine.interval) used by
	// every worker and watch that does not set its own.
	Interval time.Duration
	// StartupDelay holds the daemon for this long before starting any worker,
	// giving the host time to finish booting so services that are still coming
	// up are not flagged or remediated prematurely. <=0 disables the wait.
	StartupDelay time.Duration
}

// cycler is anything the scheduler ticks once per interval. cycleTarget names
// it for the panic-recovery log so one target's crash is attributable.
type cycler interface {
	RunCycle(ctx context.Context)
	cycleTarget() string
}

// Run starts every worker and watch and blocks until ctx is cancelled and all of
// them have returned (graceful shutdown). Workers and watches each run on their
// own goroutine at their own interval; concurrency between operations on the
// same service is bounded by that service's operation lock, not by any
// fleet-wide limit. The monitor owns final shutdown readiness.
//
// firstCycles is the number of settling keys still pending for this generation
// (Settling.Pending right after the generation armed it). The caller counts
// them from the same snapshot it armed Settling with because each target's
// pause state is read from the store and can change in between: a second read
// could count a target whose first cycle never reports, wedging /readyz at
// "starting", or miss one and open the gate early.
func (s Scheduler) Run(ctx context.Context, workers []*Worker, watches []*Watch, ready *Readiness, firstCycles int) {
	interval := s.Interval
	if interval <= 0 {
		interval = config.DefaultEngineInterval
	}

	// Grace period before the first cycle so a still-booting host can settle.
	// A cancelled generation during the wait returns without starting workers.
	// That is a shutdown or a config reload; the monitor tells them apart, so it
	// alone marks shutting_down and carries the rest of the wait into a reload.
	if s.StartupDelay > 0 {
		if !ctxutil.Sleep(ctx, s.StartupDelay) {
			return
		}
	}

	// While the daemon is starting, hold it there until every pending target has
	// completed its startup observation cycle (workers and watches call
	// Settling.MarkObserved when ready). Paused/disabled targets are not pending.
	// A daemon that is already ready (config reload) ignores the gate.
	if ready != nil {
		ready.ExpectFirstCycles(firstCycles)
	}

	// Stagger the first cycle of the whole fleet (workers + watches, including the
	// slow app-watches) across one general interval, ignoring each target's own
	// interval for that first cycle only. This avoids a startup stampede — every
	// app probe firing at once — while still checking everything within ~one
	// interval; runCycler then reverts each target to its own cadence.
	staggerTotal := len(workers) + len(watches)
	var wg sync.WaitGroup
	idx := 0
	// launch takes the target's own interval; a target that configures none
	// falls back to the engine cadence. Workers are launched before watches so
	// the stagger slots stay in the documented order.
	launch := func(c cycler, own time.Duration) {
		if own <= 0 {
			own = interval
		}
		offset := staggerOffset(idx, staggerTotal, interval)
		idx++
		wg.Go(func() {
			runCycler(ctx, c, own, offset)
		})
	}
	for _, w := range workers {
		launch(w, w.Interval)
	}
	for _, wt := range watches {
		launch(wt, wt.Interval)
	}
	wg.Wait()
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
	return time.Duration(int64(interval) * int64(idx) / int64(total))
}

// runCycler ticks a cycler from cycle completion: jitter, then cycle, then wait
// one interval, repeat. A cancelled context stops between cycles (never start a
// new operation during shutdown).
func runCycler(ctx context.Context, c cycler, interval, offset time.Duration) {
	if offset > 0 {
		if !ctxutil.Sleep(ctx, offset) {
			return
		}
	}
	for {
		if ctx.Err() != nil {
			return
		}
		runCycleGuarded(ctx, c)
		if !ctxutil.Sleep(ctx, interval) {
			return
		}
	}
}

// runCycleGuarded runs one cycle with a panic barrier: an unrecovered panic in
// any goroutine crashes the whole process, so a defect in one service's rules,
// operation, hook or notify path must not take the daemon down for the entire
// fleet. Recover it, log it, and let the next cycle proceed. Built checks
// recover separately at their shared checks.Execute boundary.
func runCycleGuarded(ctx context.Context, c cycler) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("recovered panic in monitor cycle",
				"target", c.cycleTarget(), "panic", r, "stack", string(debug.Stack()))
		}
	}()
	c.RunCycle(ctx)
}
