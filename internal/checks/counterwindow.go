package checks

import "time"

// counterWindow is the sliding sample window behind every "did this integer
// counter grow?" check. It answers growth over wall-clock time rather than since
// the previous cycle, which is what makes such a check composable: a per-cycle
// delta is true for exactly one cycle, so a rule's `for:` window would suppress it
// forever, and a per-check `interval:` would smear it across the reused results of
// the skipped cycles.
//
// It is held by pointer from the built check because a worker builds its checks
// once and ticks them sequentially — the same reason the oom counter keeps its
// previous sample in the instance. It is not persisted, so a config reload or a
// daemon restart re-baselines, exactly as those checks already do.
type counterWindow struct {
	samples []counterSample
}

type counterSample struct {
	at    time.Time
	count int
}

// advance records one sample and reports the rise since the oldest sample still
// inside window, plus the span that covers. The growth is raw: a counter that fell
// yields a negative number, and each caller decides what that means — a directory
// that shrank is not growth, and a stray count that fell was cleaned up.
//
// A first sample reports zero growth over a zero span: there is nothing to compare
// against yet, so the cycle is a baseline rather than a verdict.
func (w *counterWindow) advance(now time.Time, count int, window time.Duration) (growth int, span time.Duration) {
	w.samples = pruneWindow(w.samples, now.Add(-window), func(s counterSample) time.Time { return s.at })
	w.samples = append(w.samples, counterSample{at: now, count: count})
	baseline := w.samples[0]
	return count - baseline.count, now.Sub(baseline.at)
}

// windowClock resolves an injectable clock, so a growth check can be tested
// without sleeping. Nil means the real one.
func windowClock(clock func() time.Time) func() time.Time {
	if clock == nil {
		return time.Now
	}
	return clock
}
