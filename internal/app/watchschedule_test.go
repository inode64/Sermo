package app

import (
	"context"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"sermo/internal/checks"
)

// refreshTestInterval keeps scheduled cycles out of a test's window: after
// the first (staggered) cycle, only a refresh runs a watch again.
const refreshTestInterval = time.Hour

// refreshTestGap shortens the rate window kept before a refreshed cycle, so
// tests that refresh right after a cycle do not wait the production gap.
const refreshTestGap = time.Millisecond

// startRefreshScheduler runs watches under a scheduler that tracks them in a
// new registry until the test ends, and returns the registry and a stop func
// that waits for the scheduler.
func startRefreshScheduler(t *testing.T, watches ...*Watch) (refresh *WatchSchedule, stop func()) {
	t.Helper()
	return startRefreshSchedulerWithGap(t, refreshTestGap, watches...)
}

func startRefreshSchedulerWithGap(t *testing.T, gap time.Duration, watches ...*Watch) (refresh *WatchSchedule, stop func()) {
	t.Helper()
	refresh = NewWatchSchedule()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Scheduler{Interval: 10 * time.Millisecond, Schedule: refresh, refreshGap: gap}.Run(ctx, nil, watches, nil, 0)
	}()
	stop = func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return refresh, stop
}

func countingWatch(name string, runs *atomic.Int32) *Watch {
	return &Watch{
		Name:     name,
		Interval: refreshTestInterval,
		Check: checkFunc(func(context.Context) checks.Result {
			runs.Add(1)
			return checks.Result{OK: true}
		}),
	}
}

// waitRuns waits for runs to reach want: the staggered first cycles of a
// scheduler start shortly after Run.
func waitRuns(t *testing.T, runs *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for runs.Load() < want {
		select {
		case <-deadline:
			t.Fatalf("runs = %d, want %d", runs.Load(), want)
		case <-time.After(time.Millisecond):
		}
	}
}

func refreshByName(names ...string) func(*Watch) bool {
	return func(w *Watch) bool { return slices.Contains(names, w.Name) }
}

func TestWatchRefreshRunsTheWatchCycleNow(t *testing.T) {
	var aRuns, bRuns atomic.Int32
	refresh, _ := startRefreshScheduler(t, countingWatch("a", &aRuns), countingWatch("b", &bRuns))
	waitRuns(t, &aRuns, 1)
	waitRuns(t, &bRuns, 1)

	if out := refresh.Refresh(t.Context(), refreshByName("a")); !reflect.DeepEqual(out, WatchRefreshOutcome{Refreshed: []string{"a"}}) {
		t.Fatalf("outcome = %+v, want only a refreshed", out)
	}
	if aRuns.Load() != 2 || bRuns.Load() != 1 {
		t.Fatalf("runs a=%d b=%d, want one extra cycle for a only", aRuns.Load(), bRuns.Load())
	}
}

// A request that arrives while a cycle runs must not be answered by it: that
// cycle may have sampled before whatever the operator just fixed.
func TestWatchRefreshAnswersWithACycleStartedAfterTheRequest(t *testing.T) {
	var runs atomic.Int32
	inCycle := make(chan struct{})
	release := make(chan struct{})
	w := &Watch{Name: "slow", Interval: refreshTestInterval, Check: checkFunc(func(context.Context) checks.Result {
		if runs.Add(1) == 1 {
			close(inCycle)
			<-release
		}
		return checks.Result{OK: true}
	})}
	refresh, _ := startRefreshScheduler(t, w)
	<-inCycle

	result := make(chan WatchRefreshOutcome, 1)
	go func() { result <- refresh.Refresh(t.Context(), refreshByName("slow")) }()
	select {
	case out := <-result:
		t.Fatalf("refresh returned %+v during the cycle that predates it", out)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if out := <-result; !reflect.DeepEqual(out, WatchRefreshOutcome{Refreshed: []string{"slow"}}) || runs.Load() != 2 {
		t.Fatalf("outcome = %+v after %d runs, want refreshed by a second cycle", out, runs.Load())
	}
}

func TestWatchRefreshSkipsAPausedWatch(t *testing.T) {
	var runs atomic.Int32
	w := countingWatch("paused", &runs)
	var paused atomic.Bool
	w.IsPaused = paused.Load
	refresh, _ := startRefreshScheduler(t, w)
	waitRuns(t, &runs, 1)
	paused.Store(true)

	if out := refresh.Refresh(t.Context(), refreshByName("paused")); !reflect.DeepEqual(out, WatchRefreshOutcome{Paused: []string{"paused"}}) {
		t.Fatalf("outcome = %+v, want paused", out)
	}
	if runs.Load() != 1 {
		t.Fatalf("runs = %d, a paused watch must not be cycled", runs.Load())
	}
}

func TestWatchRefreshReportsACycleStillRunningAtTheDeadline(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	w := &Watch{Name: "hung", Interval: refreshTestInterval, Check: checkFunc(func(context.Context) checks.Result {
		if runs.Add(1) > 1 {
			<-release
		}
		return checks.Result{OK: true}
	})}
	refresh, _ := startRefreshScheduler(t, w)
	waitRuns(t, &runs, 1)
	defer close(release)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if out := refresh.Refresh(ctx, refreshByName("hung")); !reflect.DeepEqual(out, WatchRefreshOutcome{Pending: []string{"hung"}}) {
		t.Fatalf("outcome = %+v, want pending", out)
	}
}

func TestWatchRefreshDoesNotWaitForAStoppedGeneration(t *testing.T) {
	var runs atomic.Int32
	refresh, stop := startRefreshScheduler(t, countingWatch("gone", &runs))
	waitRuns(t, &runs, 1)
	stop()

	if out := refresh.Refresh(t.Context(), refreshByName("gone")); !reflect.DeepEqual(out, WatchRefreshOutcome{NotRunning: true}) {
		t.Fatalf("outcome = %+v, a stopped generation tracks nothing", out)
	}
	if len(refresh.targets) != 0 {
		t.Fatalf("targets = %d after the generation stopped", len(refresh.targets))
	}
}

// A metric watch runs one runtime per metric under one name: the name is
// refreshed only once every runtime cycled.
func TestWatchRefreshCyclesEveryRuntimeOfOneWatch(t *testing.T) {
	var first, second atomic.Int32
	refresh, _ := startRefreshScheduler(t, countingWatch("net", &first), countingWatch("net", &second))
	waitRuns(t, &first, 1)
	waitRuns(t, &second, 1)

	out := refresh.Refresh(t.Context(), refreshByName("net"))
	if !reflect.DeepEqual(out, WatchRefreshOutcome{Refreshed: []string{"net"}}) || first.Load() != 2 || second.Load() != 2 {
		t.Fatalf("outcome = %+v runs=%d/%d, want both runtimes cycled once", out, first.Load(), second.Load())
	}
}

// Refreshes of whole panels must not stampede: together, concurrent requests
// never cycle more watches at once than watchRefreshParallelism.
func TestWatchRefreshBoundsConcurrentCycles(t *testing.T) {
	var refreshing atomic.Bool
	var running, peak, started atomic.Int32
	watches := make([]*Watch, 0, 3*watchRefreshParallelism)
	names := make([]string, 0, cap(watches))
	for i := range cap(watches) {
		name := string(rune('a' + i))
		names = append(names, name)
		watches = append(watches, &Watch{Name: name, Interval: refreshTestInterval, Check: checkFunc(func(context.Context) checks.Result {
			started.Add(1)
			if refreshing.Load() {
				now := running.Add(1)
				for {
					old := peak.Load()
					if now <= old || peak.CompareAndSwap(old, now) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
				running.Add(-1)
			}
			return checks.Result{OK: true}
		})})
	}
	refresh, _ := startRefreshScheduler(t, watches...)
	waitRuns(t, &started, int32(len(watches)))
	refreshing.Store(true)

	half := len(names) / 2
	outcomes := make(chan WatchRefreshOutcome, 2)
	for _, part := range [][]string{names[:half], names[half:]} {
		go func() { outcomes <- refresh.Refresh(t.Context(), refreshByName(part...)) }()
	}
	if got := len((<-outcomes).Refreshed) + len((<-outcomes).Refreshed); got != len(watches) {
		t.Fatalf("refreshed %d watches across two requests, want all %d", got, len(watches))
	}
	if got := peak.Load(); got < 1 || got > watchRefreshParallelism {
		t.Fatalf("peak concurrent refresh cycles = %d, want 1..%d", got, watchRefreshParallelism)
	}
}

func TestWatchRefreshNilRegistryFindsNothing(t *testing.T) {
	var refresh *WatchSchedule
	wake, release := refresh.track(&Watch{Name: "x"})
	release()
	if wake != nil {
		t.Fatal("a nil registry must hand the cycler a nil wake channel")
	}
	if out := refresh.Refresh(t.Context(), func(*Watch) bool { return true }); !reflect.DeepEqual(out, WatchRefreshOutcome{NotRunning: true}) {
		t.Fatalf("outcome = %+v, want empty", out)
	}
}

// Rate checks divide by the time between two samples: a refreshed cycle that
// follows the previous one by milliseconds would read one clock tick as tens
// of percent, so it waits for the gap after the previous cycle ended.
func TestWatchRefreshKeepsARateWindowAfterThePreviousCycle(t *testing.T) {
	const gap = 50 * time.Millisecond
	var runs atomic.Int32
	var firstEnded, secondStarted atomic.Int64
	w := &Watch{Name: "rates", Interval: refreshTestInterval, Check: checkFunc(func(context.Context) checks.Result {
		if runs.Add(1) == 1 {
			firstEnded.Store(time.Now().UnixNano())
		} else {
			secondStarted.Store(time.Now().UnixNano())
		}
		return checks.Result{OK: true}
	})}
	refresh, _ := startRefreshSchedulerWithGap(t, gap, w)
	waitRuns(t, &runs, 1)

	if out := refresh.Refresh(t.Context(), refreshByName("rates")); !reflect.DeepEqual(out, WatchRefreshOutcome{Refreshed: []string{"rates"}}) {
		t.Fatalf("outcome = %+v, want refreshed", out)
	}
	if got := time.Duration(secondStarted.Load() - firstEnded.Load()); got < gap {
		t.Fatalf("refreshed cycle started %s after the previous one, want at least %s", got, gap)
	}
}

// Once the deadline has passed no new cycle starts, so no action runs after
// the outcome reported the watch as not refreshed.
func TestWatchRefreshStartsNothingAfterTheDeadline(t *testing.T) {
	var runs atomic.Int32
	refresh, _ := startRefreshScheduler(t, countingWatch("late", &runs))
	waitRuns(t, &runs, 1)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if out := refresh.Refresh(ctx, refreshByName("late")); !reflect.DeepEqual(out, WatchRefreshOutcome{Pending: []string{"late"}}) {
		t.Fatalf("outcome = %+v, want pending", out)
	}
	time.Sleep(20 * time.Millisecond)
	if runs.Load() != 1 {
		t.Fatalf("runs = %d, a refresh past its deadline started a cycle", runs.Load())
	}
}

// Before any scheduler run has started its watches (engine.startup_delay, a
// reload) a refresh could run nothing, which is not the same as nothing to run.
func TestWatchRefreshReportsNoRunningGeneration(t *testing.T) {
	if out := NewWatchSchedule().Refresh(t.Context(), func(*Watch) bool { return true }); !out.NotRunning {
		t.Fatalf("outcome = %+v, want NotRunning before any scheduler run", out)
	}
	var runs atomic.Int32
	refresh, stop := startRefreshScheduler(t, countingWatch("a", &runs))
	waitRuns(t, &runs, 1)
	if out := refresh.Refresh(t.Context(), func(*Watch) bool { return false }); out.NotRunning {
		t.Fatalf("outcome = %+v, want a running generation", out)
	}
	stop()
	if out := refresh.Refresh(t.Context(), func(*Watch) bool { return true }); !out.NotRunning {
		t.Fatalf("outcome = %+v, want NotRunning once the generation stopped", out)
	}
}

// timedWatch records when each of its first cycles started.
func timedWatch(name string, interval time.Duration, runs *atomic.Int32, starts []atomic.Int64) *Watch {
	var started atomic.Int32
	return &Watch{Name: name, Interval: interval, Check: checkFunc(func(context.Context) checks.Result {
		if n := int(started.Add(1)); n <= len(starts) {
			starts[n-1].Store(time.Now().UnixNano())
		}
		// Publish completion only after the timestamp used by assertions exists.
		runs.Add(1)
		return checks.Result{OK: true}
	})}
}

// A refresh is an extra cycle: the watch's schedule stays where it was, so a
// check due later still runs when it was due, not one interval after the
// refresh.
func TestWatchRefreshKeepsTheSchedule(t *testing.T) {
	const interval = 600 * time.Millisecond
	var runs atomic.Int32
	starts := make([]atomic.Int64, 3)
	refresh, _ := startRefreshScheduler(t, timedWatch("timed", interval, &runs, starts))
	waitRuns(t, &runs, 1)
	time.Sleep(interval / 2)
	if out := refresh.Refresh(t.Context(), refreshByName("timed")); !reflect.DeepEqual(out, WatchRefreshOutcome{Refreshed: []string{"timed"}}) {
		t.Fatalf("outcome = %+v, want refreshed", out)
	}
	waitRuns(t, &runs, 3)
	// Moved, the scheduled cycle would come about 1.5 intervals after the first.
	if got := time.Duration(starts[2].Load() - starts[0].Load()); got < interval-interval/10 || got > interval+interval/4 {
		t.Fatalf("scheduled cycle came %s after the first, want about %s", got, interval)
	}
}

// A scheduled cycle that comes due right after a refreshed one waits for the
// gap too: its rate window would be as short.
func TestWatchRefreshHoldsAScheduledCycleDueRightAfterIt(t *testing.T) {
	const interval = 200 * time.Millisecond
	const gap = 150 * time.Millisecond
	var runs atomic.Int32
	starts := make([]atomic.Int64, 3)
	refresh, _ := startRefreshSchedulerWithGap(t, gap, timedWatch("tight", interval, &runs, starts))
	waitRuns(t, &runs, 1)
	refresh.Refresh(t.Context(), refreshByName("tight"))
	waitRuns(t, &runs, 3)
	if got := time.Duration(starts[2].Load() - starts[1].Load()); got < gap {
		t.Fatalf("scheduled cycle came %s after the refreshed one, want at least %s", got, gap)
	}
}

// The schedule reports when each watch's next cycle starts, and a refresh, an
// extra cycle, leaves that time where it was.
func TestWatchScheduleReportsTheNextCheckAndRefreshKeepsIt(t *testing.T) {
	var runs atomic.Int32
	w := countingWatch("disk", &runs)
	w.CheckType = "disk"
	schedule, _ := startRefreshScheduler(t, w)
	waitRuns(t, &runs, 1)
	key := scheduleKey{name: "disk", checkType: "disk"}
	var next time.Time
	deadline := time.After(2 * time.Second)
	for next.IsZero() {
		next = schedule.nextChecks()[key]
		select {
		case <-deadline:
			t.Fatal("no next check recorded after the first cycle")
		case <-time.After(time.Millisecond):
		}
	}
	if until := time.Until(next); until < refreshTestInterval-time.Minute || until > refreshTestInterval {
		t.Fatalf("next check in %s, want about %s", until, refreshTestInterval)
	}
	schedule.Refresh(t.Context(), refreshByName("disk"))
	if got := schedule.nextChecks()[key]; !got.Equal(next) {
		t.Fatalf("next check after a refresh = %s, want it kept at %s", got, next)
	}
}
