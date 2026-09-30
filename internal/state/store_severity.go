package state

import "sermo/internal/rules"

// columnSeverityWindows names the JSON-encoded severity rungs in errors.
const columnSeverityWindows = "severity windows"

// severityWindowJSON is one persisted severity rung: the entry-window progress
// of a graded owner at one level.
type severityWindowJSON struct {
	Consecutive  int     `json:"consecutive,omitempty"`
	History      []bool  `json:"history,omitempty"`
	TrueSince    int64   `json:"true_since,omitempty"`
	TimedHistory []int64 `json:"timed_history,omitempty"`
}

// encodeSeverityWindows stores a graded owner's rungs; an owner that never
// graded a sample stores the empty string, which restores as "seed on the
// next graded sample".
func encodeSeverityWindows(rungs []rules.EntryWindowSnapshot) (string, error) {
	if rungs == nil {
		return "", nil
	}
	return encodeRows(columnSeverityWindows, rungs, func(r rules.EntryWindowSnapshot) (severityWindowJSON, bool) {
		return severityWindowJSON{Consecutive: r.Consecutive, History: r.History, TrueSince: timeUnixNano(r.TrueSince), TimedHistory: sampleNanos(r.TimedHistory)}, true
	})
}

func decodeSeverityWindows(raw string) ([]rules.EntryWindowSnapshot, error) {
	return decodeRows(columnSeverityWindows, raw, func(r severityWindowJSON) (rules.EntryWindowSnapshot, bool) {
		return rules.EntryWindowSnapshot{Consecutive: r.Consecutive, History: r.History, TrueSince: unixNanoTime(r.TrueSince), TimedHistory: nanoSamples(r.TimedHistory)}, true
	})
}

// sampleNanos stores window samples as unix nanoseconds, dropping any that
// lost its timestamp.
func sampleNanos(samples []rules.WindowSample) []int64 {
	nanos := make([]int64, 0, len(samples))
	for _, sample := range samples {
		if !sample.At.IsZero() {
			nanos = append(nanos, timeUnixNano(sample.At))
		}
	}
	return nanos
}

// nanoSamples restores window samples; nil when none survive.
func nanoSamples(nanos []int64) []rules.WindowSample {
	var samples []rules.WindowSample
	for _, at := range nanos {
		if at != 0 {
			samples = append(samples, rules.WindowSample{At: unixNanoTime(at)})
		}
	}
	return samples
}

// severityWindowsIdle reports rungs that carry no progress, so a watch with
// nothing to remember can still delete its runtime row.
func severityWindowsIdle(rungs []rules.EntryWindowSnapshot) bool {
	for _, r := range rungs {
		if r.Consecutive != 0 || len(r.History) != 0 || !r.TrueSince.IsZero() || len(r.TimedHistory) != 0 {
			return false
		}
	}
	return true
}
