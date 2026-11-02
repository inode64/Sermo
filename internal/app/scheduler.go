package app

import (
	"cmp"
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"sermo/internal/config"
	"sermo/internal/ctxutil"
)

// Scheduler runs each worker on its own goroutine with an independent interval
// timer measured from cycle completion (so an overrun delays the next cycle,
// never queues one) and staggers the workers' first cycles evenly across the
// interval. Operation concurrency is bounded
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
	// Schedule, when set, tracks every watch this run schedules: when its next
	// cycle is due, and an operator's request for an extra cycle now (see
	// WatchSchedule). nil keeps every watch untracked on its own interval.
	// Service workers are never tracked.
	Schedule *WatchSchedule
	// refreshGap overrides refreshedCycleGap; zero uses it. Tests shorten it.
	refreshGap time.Duration
}

// refreshedCycleGap is the least time a refreshed cycle starts after the
// previous cycle of the same watch ended. Rate checks (process CPU and I/O,
// net, disk I/O) divide by the time between two samples: back-to-back cycles a
// few milliseconds apart would read one clock tick as tens of percent and fire
// an action on noise. It is the window a manual disk I/O probe opens.
const refreshedCycleGap = defaultDiskIOProbeWindow

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
	gap := cmp.Or(s.refreshGap, refreshedCycleGap)
	endGeneration := s.Schedule.beginGeneration()
	defer endGeneration()
	// launch takes the target's own interval; a target that configures none
	// falls back to the engine cadence. Workers are launched before watches so
	// the stagger slots stay in the documented order.
	launch := func(c cycler, own time.Duration, entry *scheduledWatch, release func()) {
		if own <= 0 {
			own = interval
		}
		offset := staggerOffset(idx, staggerTotal, interval)
		idx++
		wg.Go(func() {
			defer release()
			runCycler(ctx, c, own, offset, entry, min(gap, own))
		})
	}
	for _, w := range workers {
		launch(w, w.Interval, nil, func() {})
	}
	for _, wt := range watches {
		entry, release := s.Schedule.track(wt)
		launch(wt, wt.Interval, entry, release)
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

// runCycler ticks a cycler from cycle completion: stagger offset, then cycle, then wait
// one interval, repeat. A refresh request on the entry runs an extra cycle,
// never sooner than gap after the previous one ended, and leaves the schedule
// where it was: a check due in two hours still runs in two hours. Each request
// is released once the cycle answering it completes, and the entry records
// when the next scheduled cycle starts. A cancelled context stops between
// cycles (never start a new operation during shutdown).
func runCycler(ctx context.Context, c cycler, interval, offset time.Duration, entry *scheduledWatch, gap time.Duration) {
	due := time.Now().Add(offset)
	var ended time.Time
	for {
		earliest := ended.Add(gap)
		// A scheduled cycle that comes due within the gap after a refreshed one
		// waits for it too: its rate window would be as short.
		next := due
		if earliest.After(next) {
			next = earliest
		}
		entry.setDue(next)
		answered, ok := awaitCycle(ctx, next, entry.requests(), earliest)
		if !ok || ctx.Err() != nil {
			return
		}
		runCycleGuarded(ctx, c)
		ended = time.Now()
		for _, done := range answered {
			close(done)
		}
		if answered == nil {
			due = ended.Add(interval)
		}
	}
}

// awaitCycle waits until the next cycle may start: the scheduled one at due, or
// a refresh request once earliest has passed. It returns the requests the
// cycle answers (none for a scheduled cycle), and false when ctx ends first.
func awaitCycle(ctx context.Context, due time.Time, wake <-chan chan struct{}, earliest time.Time) ([]chan struct{}, bool) {
	timer := time.NewTimer(time.Until(due))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, false
	case <-timer.C:
		return nil, true
	case done := <-wake:
		return gatherRefreshes(ctx, wake, done, earliest)
	}
}

// gatherRefreshes holds a refreshed cycle until earliest and lets every
// request that arrives meanwhile, or is already waiting, join it, so
// concurrent refreshes share one cycle instead of queueing one each.
func gatherRefreshes(ctx context.Context, wake <-chan chan struct{}, first chan struct{}, earliest time.Time) ([]chan struct{}, bool) {
	answered := []chan struct{}{first}
	hold := time.NewTimer(max(time.Until(earliest), 0))
	defer hold.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case more := <-wake:
			answered = append(answered, more)
		case <-hold.C:
			for {
				select {
				case more := <-wake:
					answered = append(answered, more)
				default:
					return answered, true
				}
			}
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
