package checks

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"sermo/internal/cfgval"
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

// fieldIncrease is one max_increase bound: the value of field may rise by at most
// limit within the check's window. path is field compiled for a JSON document (a
// command check's stdout); a connection check reads field from its flat probe
// result and leaves it nil. state holds the sliding samples; being a pointer, it
// survives across cycles and re-baselines when the check is rebuilt.
type fieldIncrease struct {
	field string
	path  []string
	limit float64
	state *counterWindow
}

// fieldGrowth is one field's reading for a cycle: its current value and the rise
// since the oldest sample still inside the window.
type fieldGrowth struct {
	field   string
	limit   float64
	current int
	growth  int
	span    time.Duration
	missing bool
}

// parseFieldIncreases parses max_increase (field -> non-negative integer; 0
// fails on any rise) and its within span. Growth is measured over wall-clock
// time, not cycles, so one requires the other — the same contract as the
// strays check. compile turns each field into its lookup path; nil keeps the
// field as a flat key.
func parseFieldIncreases(entry map[string]any, compile func(string) ([]string, error)) ([]fieldIncrease, time.Duration, string) {
	raw, present := entry[CheckKeyMaxIncrease]
	if !present {
		if _, hasWindow := entry[CheckKeyWithin]; hasWindow {
			return nil, 0, CheckKeyWithin + " requires " + CheckKeyMaxIncrease
		}
		return nil, 0, ""
	}
	m, ok := raw.(map[string]any)
	if !ok || len(m) == 0 {
		return nil, 0, CheckKeyMaxIncrease + " must be a mapping of field -> non-negative integer"
	}
	window := cfgval.Duration(entry[CheckKeyWithin])
	if window <= 0 {
		return nil, 0, CheckKeyMaxIncrease + " requires " + CheckKeyWithin + " as a positive duration"
	}
	out := make([]fieldIncrease, 0, len(m))
	for _, field := range slices.Sorted(maps.Keys(m)) {
		n, ok := cfgval.Int(m[field])
		if !ok || n < 0 {
			return nil, 0, CheckKeyMaxIncrease + "." + field + " must be a non-negative integer"
		}
		inc := fieldIncrease{field: field, limit: float64(n), state: &counterWindow{}}
		if compile != nil {
			path, err := compile(field)
			if err != nil {
				return nil, 0, CheckKeyMaxIncrease + ": " + err.Error()
			}
			inc.path = path
		}
		out = append(out, inc)
	}
	return out, window, ""
}

// sampleFieldIncreases advances every max_increase window with this cycle's
// value, read by lookup. A field that is absent or has no integer reading is
// reported as missing and leaves its window untouched.
func sampleFieldIncreases(increases []fieldIncrease, window time.Duration, clock func() time.Time, lookup func(fieldIncrease) (any, bool)) []fieldGrowth {
	if len(increases) == 0 {
		return nil
	}
	now := windowClock(clock)()
	out := make([]fieldGrowth, 0, len(increases))
	for _, inc := range increases {
		g := fieldGrowth{field: inc.field, limit: inc.limit}
		current, ok := growthSampleValue(lookup(inc))
		if !ok {
			g.missing = true
			out = append(out, g)
			continue
		}
		g.current = current
		rise, span := inc.state.advance(now, current, window)
		// A value can legitimately fall — keys expire, a queue drains — so only a
		// rise is growth.
		g.growth, g.span = max(rise, 0), span
		out = append(out, g)
	}
	return out
}

// growthSampleValue reads a sampled value as the integer a window stores. NaN, an
// infinity or a magnitude past the int range has no such reading: converting it
// anyway yields an arbitrary integer that would read as a huge rise or fall.
func growthSampleValue(raw any, present bool) (int, bool) {
	if !present {
		return 0, false
	}
	value, ok := cfgval.Float(raw)
	if !ok {
		return 0, false
	}
	value = math.Round(value)
	if math.IsNaN(value) || value >= math.MaxInt || value < math.MinInt {
		return 0, false
	}
	return int(value), true
}

// increaseFailure returns the first max_increase bound that does not hold ("" when
// all do), plus whether it failed for lack of a usable value.
func increaseFailure(growths []fieldGrowth) (string, bool) {
	for _, g := range growths {
		if g.missing {
			return fmt.Sprintf("field %q not available as a number for %s", g.field, CheckKeyMaxIncrease), true
		}
		if float64(g.growth) > g.limit {
			return fmt.Sprintf("%s grew by %d in %s (%s %s): %d now", g.field, g.growth,
				g.span.Round(time.Second), CheckKeyMaxIncrease, formatThreshold(g.limit), g.current), false
		}
	}
	return "", false
}

// increaseData records each sampled field's rise as <field>_increase plus the
// window it is measured over, allocating data when the result carries none.
func increaseData(data map[string]any, growths []fieldGrowth, window time.Duration) map[string]any {
	if len(growths) == 0 {
		return data
	}
	if data == nil {
		data = map[string]any{}
	}
	for _, g := range growths {
		if !g.missing {
			data[g.field+DataKeyIncreaseSuffix] = g.growth
		}
	}
	data[DataKeyWindow] = window.String()
	return data
}
