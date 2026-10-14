package checks

import (
	"slices"
	"time"
)

// growthWindow is the sliding sample window behind every "did this value grow?"
// check — an integer counter (strays, files, connection stats) or a byte size.
// It answers growth over wall-clock time rather than since the previous cycle,
// which is what makes such a check composable: a per-cycle delta is true for
// exactly one cycle, so a rule's `for:` window would suppress it forever, and a
// per-check `interval:` would smear it across the reused results of the skipped
// cycles.
//
// It is held by pointer from the built check because a worker builds its checks
// once and ticks them sequentially — the same reason the oom counter keeps its
// previous sample in the instance. It is not persisted, so a config reload or a
// daemon restart re-baselines, exactly as those checks already do.
type growthWindow[T ~int | ~int64] struct {
	samples []growthSample[T]
}

type growthSample[T ~int | ~int64] struct {
	at    time.Time
	value T
}

// counterWindow tracks an integer count; byteWindow tracks a byte total.
type (
	counterWindow = growthWindow[int]
	byteWindow    = growthWindow[int64]
)

// advance records one sample and reports the rise since the oldest sample still
// inside window, plus the span that covers. The growth is raw: a value that fell
// yields a negative number, and each caller decides what that means — a directory
// that shrank is not growth, and a stray count that fell was cleaned up.
//
// A first sample reports zero growth over a zero span: there is nothing to compare
// against yet, so the cycle is a baseline rather than a verdict.
//
// When every earlier sample has slid out of window, the newest of them stays as
// the baseline. A window no longer than the sampling interval (within: 30s on
// the default 30s tick, which lands a few ms late) would otherwise baseline on
// the current sample every cycle and report +0 forever.
func (w *growthWindow[T]) advance(now time.Time, value T, window time.Duration) (growth T, span time.Duration) {
	cutoff := now.Add(-window)
	// Samples are appended in time order, so a stale newest one means every
	// earlier sample is stale too.
	if last := len(w.samples) - 1; last >= 0 && w.samples[last].at.Before(cutoff) {
		w.samples = w.samples[last:]
	} else {
		w.samples = slices.DeleteFunc(w.samples, func(s growthSample[T]) bool { return s.at.Before(cutoff) })
	}
	w.samples = append(w.samples, growthSample[T]{at: now, value: value})
	baseline := w.samples[0]
	return value - baseline.value, now.Sub(baseline.at)
}

// windowClock resolves an injectable clock, so a growth check can be tested
// without sleeping. Nil means the real one.
func windowClock(clock func() time.Time) func() time.Time {
	if clock == nil {
		return time.Now
	}
	return clock
}
