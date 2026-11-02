package app

import (
	"context"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// watchRefreshParallelism bounds how many watches the daemon's refreshes cycle
// at once, across every concurrent request. The scheduler staggers first cycles
// so app version probes never all fire together, and those probes take no
// shared check slot: refreshes of whole panels must not undo that stagger.
const watchRefreshParallelism = 4

// WatchSchedule is the scheduler's record of the watches it runs, kept for the
// life of each generation: when every watch's next cycle is due, and an
// operator's request for an extra cycle now. The refreshed cycle is the
// watch's own, run on its own goroutine, so it never overlaps a scheduled one:
// it counts toward cycle-based for/within windows and dispatches the watch's
// actions like any other cycle, and the watch's schedule stays where it was.
// One daemon shares one registry across reloads.
type WatchSchedule struct {
	mu      sync.Mutex
	targets map[*scheduledWatch]struct{}
	// generations counts the scheduler runs past engine.startup_delay; none
	// means a refresh could not run anything, not that there was nothing to run.
	generations int
	slots       chan struct{}
}

// scheduledWatch is one tracked watch, its request channel and when its next
// cycle is due. A request carries the channel the cycler closes once the cycle
// that answers it has completed; stopped closes when the cycler exits.
type scheduledWatch struct {
	watch   *Watch
	wake    chan chan struct{}
	stopped chan struct{}
	due     atomic.Int64 // unix nanoseconds; zero until the cycler sets it
}

// requests is the channel the cycler receives refresh requests on; an
// untracked cycler (nil) gets a nil channel, which never delivers.
func (t *scheduledWatch) requests() <-chan chan struct{} {
	if t == nil {
		return nil
	}
	return t.wake
}

// setDue records when the cycler starts its next cycle.
func (t *scheduledWatch) setDue(at time.Time) {
	if t != nil {
		t.due.Store(at.UnixNano())
	}
}

// NewWatchSchedule returns an empty registry.
func NewWatchSchedule() *WatchSchedule {
	return &WatchSchedule{targets: map[*scheduledWatch]struct{}{}, slots: make(chan struct{}, watchRefreshParallelism)}
}

// beginGeneration marks a scheduler run that has started its targets until
// the returned func runs. A nil registry marks nothing.
func (r *WatchSchedule) beginGeneration() (end func()) {
	if r == nil {
		return func() {}
	}
	r.mu.Lock()
	r.generations++
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		r.generations--
		r.mu.Unlock()
	}
}

// track registers w until release runs; the scheduler's cycler for w reads
// requests from and records its due time on the returned entry. A nil registry
// tracks nothing and returns a nil entry.
func (r *WatchSchedule) track(w *Watch) (entry *scheduledWatch, release func()) {
	if r == nil {
		return nil, func() {}
	}
	target := &scheduledWatch{watch: w, wake: make(chan chan struct{}), stopped: make(chan struct{})}
	r.mu.Lock()
	r.targets[target] = struct{}{}
	r.mu.Unlock()
	return target, func() {
		r.mu.Lock()
		delete(r.targets, target)
		r.mu.Unlock()
		close(target.stopped)
	}
}

// scheduleKey identifies a dashboard watch row: its name and check type, the
// key its readings are published under.
type scheduleKey struct{ name, checkType string }

// nextChecks returns, per watch, when its next cycle is due: the earliest of
// its runtimes (a metric watch runs one per metric). A watch the scheduler is
// not running, or whose cycler has not set a due time yet, is absent.
func (r *WatchSchedule) nextChecks() map[scheduleKey]time.Time {
	out := map[scheduleKey]time.Time{}
	if r == nil {
		return out
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for target := range r.targets {
		nanos := target.due.Load()
		if nanos == 0 {
			continue
		}
		key := scheduleKey{name: target.watch.Name, checkType: target.watch.CheckType}
		due := time.Unix(0, nanos)
		if prev, ok := out[key]; !ok || due.Before(prev) {
			out[key] = due
		}
	}
	return out
}

// WatchRefreshOutcome names, by watch, what one refresh did. A metric watch
// runs one runtime per metric under a single name; the name counts as
// refreshed only when every one of them completed a cycle.
type WatchRefreshOutcome struct {
	// Refreshed watches completed a cycle that started after the request.
	Refreshed []string
	// Paused watches are not monitored, so a cycle would sample nothing.
	Paused []string
	// Pending watches had not completed when the deadline passed or their
	// generation stopped; a cycle already started still runs to its end.
	Pending []string
	// NotRunning reports that no scheduler run had started its watches
	// (engine.startup_delay, a reload in progress), so nothing could run.
	NotRunning bool
}

// Refresh runs an extra cycle of every tracked watch match accepts and waits
// for those cycles until ctx ends. match reads only a watch's fields fixed at
// build time. Each list of the outcome is sorted by name.
func (r *WatchSchedule) Refresh(ctx context.Context, match func(*Watch) bool) WatchRefreshOutcome {
	groups, running := r.matching(match)
	out := WatchRefreshOutcome{NotRunning: !running}
	var active []string
	for _, name := range slices.Sorted(maps.Keys(groups)) {
		// Metric runtimes of one watch share its monitor key.
		if slices.ContainsFunc(groups[name], func(t *scheduledWatch) bool { return !watchTargetActive(t.watch) }) {
			out.Paused = append(out.Paused, name)
			continue
		}
		active = append(active, name)
	}
	cycled := make([]bool, len(active))
	var wg sync.WaitGroup
	for i, name := range active {
		select {
		case r.slots <- struct{}{}:
		case <-ctx.Done():
			continue
		}
		// A slot freed just as the deadline passed must not start a cycle the
		// outcome already reports as not refreshed.
		if ctx.Err() != nil {
			<-r.slots
			continue
		}
		wg.Go(func() {
			defer func() { <-r.slots }()
			cycled[i] = cycleAll(ctx, groups[name])
		})
	}
	wg.Wait()
	for i, name := range active {
		if cycled[i] {
			out.Refreshed = append(out.Refreshed, name)
		} else {
			out.Pending = append(out.Pending, name)
		}
	}
	return out
}

// matching groups the tracked watches match accepts by watch name, and
// reports whether any scheduler run has started its watches.
func (r *WatchSchedule) matching(match func(*Watch) bool) (map[string][]*scheduledWatch, bool) {
	groups := map[string][]*scheduledWatch{}
	if r == nil {
		return groups, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for target := range r.targets {
		if match(target.watch) {
			groups[target.watch.Name] = append(groups[target.watch.Name], target)
		}
	}
	return groups, r.generations > 0
}

// cycleAll cycles each runtime of one watch in turn and reports whether all of
// them completed.
func cycleAll(ctx context.Context, targets []*scheduledWatch) bool {
	for _, target := range targets {
		if !target.cycleNow(ctx) {
			return false
		}
	}
	return true
}

// cycleNow asks the target's cycler for a cycle and waits for it. A request
// that arrives while a cycle runs is answered by the next one, so the result
// is always sampled after the request.
func (t *scheduledWatch) cycleNow(ctx context.Context) bool {
	// select picks at random among ready cases: without this check an expired
	// deadline could still wake a cycle the outcome reports as not refreshed.
	if ctx.Err() != nil {
		return false
	}
	done := make(chan struct{})
	select {
	case t.wake <- done:
	case <-t.stopped:
		return false
	case <-ctx.Done():
		return false
	}
	select {
	case <-done:
		return true
	case <-t.stopped:
		// The cycler closes done before it stops when the cycle ran.
		select {
		case <-done:
			return true
		default:
			return false
		}
	case <-ctx.Done():
		return false
	}
}
