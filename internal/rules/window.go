package rules

import (
	"fmt"
	"slices"
	"time"

	"sermo/internal/cfgval"
)

// WindowSample is one observed condition result in a duration-based within
// window.
type WindowSample struct {
	At    time.Time
	Match bool
}

// WindowState tracks a rule's condition history across cycles and timestamps so
// for/within windows can be evaluated. One instance per rule per service,
// persisted by the worker between cycles.
type WindowState struct {
	consecutive  int
	history      []bool // sliding window for `within: {cycles: ...}`
	trueSince    time.Time
	timedHistory []WindowSample // true samples for `within: {duration: ...}`
}

// WindowStateSnapshot is the serializable form of a WindowState.
type WindowStateSnapshot struct {
	Consecutive  int
	History      []bool
	TrueSince    time.Time
	TimedHistory []WindowSample
}

// withinWindow returns a within-window's cycle count and effective minimum
// matches (defaulting to 1), and whether a within window is configured. It is the
// single source of the within defaults shared by Fires/IsFiring/Progress.
func (r Rule) withinWindow() (cycles int, duration time.Duration, minMatches int, ok bool) {
	if r.Within != nil && (r.Within.Cycles > 0 || r.Within.Duration > 0) {
		mm := r.Within.MinMatches
		if mm <= 0 {
			mm = 1
		}
		return r.Within.Cycles, r.Within.Duration, mm, true
	}
	return 0, 0, 0, false
}

// forWindow returns the configured consecutive cycles or duration. With no
// window the default is 1 cycle (fire the moment the condition is true).
func (r Rule) forWindow() (cycles int, duration time.Duration) {
	if r.For != nil {
		if r.For.Duration > 0 {
			return 0, r.For.Duration
		}
		if r.For.Cycles > 0 {
			return r.For.Cycles, 0
		}
	}
	return 1, 0
}

// Fires updates the window with this cycle's condition value and reports whether
// the rule fires. With neither for nor within, the default is `for 1 cycle`.
func (s *WindowState) Fires(r Rule, conditionTrue bool) bool {
	return s.FiresAt(r, conditionTrue, time.Now())
}

// FiresAt is Fires with an explicit observation time. Workers and watches use it
// so duration-based windows share the same injected clock as policy/cooldown
// logic and tests can avoid wall-clock sleeps.
func (s *WindowState) FiresAt(r Rule, conditionTrue bool, at time.Time) bool {
	if cycles, duration, minMatches, ok := r.withinWindow(); ok {
		if duration > 0 {
			if conditionTrue {
				s.timedHistory = append(s.timedHistory, WindowSample{At: at, Match: true})
			}
			s.timedHistory = recentSamples(s.timedHistory, at, duration)
			return countTimedTrue(s.timedHistory) >= minMatches
		}
		s.history = append(s.history, conditionTrue)
		s.history = s.history[max(len(s.history)-cycles, 0):]
		return countTrue(s.history) >= minMatches
	}
	_, duration := r.forWindow()
	if duration > 0 {
		if conditionTrue {
			if s.trueSince.IsZero() {
				s.trueSince = at
			}
		} else {
			s.trueSince = time.Time{}
		}
		return durationElapsed(s.trueSince, at) >= duration
	}
	cycles, _ := r.forWindow()
	if conditionTrue {
		s.consecutive++
	} else {
		s.consecutive = 0
	}
	return s.consecutive >= cycles
}

// counters returns the window's read-only counters; a nil state (a rule that
// has not ticked yet) reads as zero progress. The read-only methods go through
// this accessor instead of rebinding the receiver.
func (s *WindowState) counters() (consecutive int, history []bool, trueSince time.Time, timedHistory []WindowSample) {
	if s == nil {
		return 0, nil, time.Time{}, nil
	}
	return s.consecutive, s.history, s.trueSince, s.timedHistory
}

// IsFiring reports whether the rule would fire from the current window state
// without advancing it (read-only, nil-safe; use Fires during evaluation).
func (s *WindowState) IsFiring(r Rule) bool {
	return s.IsFiringAt(r, time.Now())
}

// IsFiringAt is IsFiring with an explicit read time for duration windows.
func (s *WindowState) IsFiringAt(r Rule, at time.Time) bool {
	consecutive, history, trueSince, timedHistory := s.counters()
	if _, duration, minMatches, ok := r.withinWindow(); ok {
		if duration > 0 {
			return countTimedTrue(recentSamples(timedHistory, at, duration)) >= minMatches
		}
		return countTrue(history) >= minMatches
	}
	cycles, duration := r.forWindow()
	if duration > 0 {
		return durationElapsed(trueSince, at) >= duration
	}
	return consecutive >= cycles
}

// Progress returns an operator-facing window counter such as "2/3" for
// consecutive windows, "2m/6m" for duration windows, or "2/3 in 15 cycles" for
// within windows. Nil-safe.
func (s *WindowState) Progress(r Rule) string {
	return s.ProgressAt(r, time.Now())
}

// ProgressAt is Progress with an explicit read time for duration windows.
func (s *WindowState) ProgressAt(r Rule, at time.Time) string {
	consecutive, history, trueSince, timedHistory := s.counters()
	if cycles, duration, minMatches, ok := r.withinWindow(); ok {
		if duration > 0 {
			return fmt.Sprintf("%d/%d in %s", countTimedTrue(recentSamples(timedHistory, at, duration)), minMatches, formatWindowDuration(duration))
		}
		return fmt.Sprintf("%d/%d in %d cycles", countTrue(history), minMatches, cycles)
	}
	cycles, duration := r.forWindow()
	if duration > 0 {
		elapsed := min(durationElapsed(trueSince, at), duration)
		return fmt.Sprintf("%s/%s", formatWindowDuration(elapsed), formatWindowDuration(duration))
	}
	return fmt.Sprintf("%d/%d", consecutive, cycles)
}

// Clone returns a deep copy of the window state for config reload.
func (s *WindowState) Clone() *WindowState {
	if s == nil {
		return nil
	}
	return WindowStateFromSnapshot(s.Snapshot())
}

// Snapshot returns a deep-copyable representation of the current window state.
func (s *WindowState) Snapshot() WindowStateSnapshot {
	if s == nil {
		return WindowStateSnapshot{}
	}
	return WindowStateSnapshot{
		Consecutive:  s.consecutive,
		History:      slices.Clone(s.history),
		TrueSince:    s.trueSince,
		TimedHistory: slices.Clone(s.timedHistory),
	}
}

// WindowStateFromSnapshot restores a window state snapshot.
func WindowStateFromSnapshot(snapshot WindowStateSnapshot) *WindowState {
	snapshot.Consecutive = max(snapshot.Consecutive, 0)
	return &WindowState{
		consecutive:  snapshot.Consecutive,
		history:      slices.Clone(snapshot.History),
		trueSince:    snapshot.TrueSince,
		timedHistory: slices.Clone(snapshot.TimedHistory),
	}
}

// WindowDescription summarizes the configured for/within window.
func WindowDescription(r Rule) string {
	if cycles, duration, minMatches, ok := r.withinWindow(); ok {
		if duration > 0 {
			return fmt.Sprintf("within %s (min %d)", formatWindowDuration(duration), minMatches)
		}
		return fmt.Sprintf("within %d cycles (min %d)", cycles, minMatches)
	}
	if r.For != nil {
		if r.For.Duration > 0 {
			return fmt.Sprintf("for %s", formatWindowDuration(r.For.Duration))
		}
		if r.For.Cycles > 0 {
			return fmt.Sprintf("for %d consecutive", r.For.Cycles)
		}
	}
	return "immediate"
}

func countTrue(history []bool) int {
	n := 0
	for _, v := range history {
		if v {
			n++
		}
	}
	return n
}

func recentSamples(history []WindowSample, at time.Time, duration time.Duration) []WindowSample {
	if duration <= 0 || len(history) == 0 {
		return history
	}
	cutoff := at.Add(-duration)
	var out []WindowSample
	for _, sample := range history {
		if sample.At.IsZero() || sample.At.Before(cutoff) || sample.At.After(at) {
			continue
		}
		out = append(out, sample)
	}
	if len(out) == len(history) {
		return history
	}
	return out
}

func countTimedTrue(history []WindowSample) int {
	n := 0
	for _, sample := range history {
		if sample.Match {
			n++
		}
	}
	return n
}

func durationElapsed(since, at time.Time) time.Duration {
	if since.IsZero() || at.Before(since) {
		return 0
	}
	return at.Sub(since)
}

func formatWindowDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int64(d/time.Hour))
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int64(d/time.Minute))
	}
	if d%time.Second == 0 {
		return fmt.Sprintf("%ds", int64(d/time.Second))
	}
	return d.String()
}

// ParseForWindow parses a `for` window ({cycles}) from a config node, or nil when
// absent. Shared by the rules parser and the host-watch builder.
func ParseForWindow(v any) *ForWindow {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	cycles, _ := cfgval.Int(m["cycles"])
	return &ForWindow{Cycles: cycles, Duration: cfgval.Duration(m["duration"])}
}

// ParseWithinWindow parses a `within` window ({cycles, min_matches}) from a config
// node, or nil when absent. min_matches defaults to 1 (true at least once within
// the window) — the same default ParseRuleWindow applies, so every `within` form
// reads identically. Shared by the rules parser and the host-watch builder.
func ParseWithinWindow(v any) *WithinWindow {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	cycles, _ := cfgval.Int(m["cycles"])
	return &WithinWindow{Cycles: cycles, Duration: cfgval.Duration(m["duration"]), MinMatches: minMatches(m)}
}

// ParseWindow parses an entry's `for`/`within` sub-blocks into their windows.
// Shared by the rules parser and the host-watch builder so both read a window the
// same way.
func ParseWindow(entry map[string]any) (*ForWindow, *WithinWindow) {
	return ParseForWindow(entry["for"]), ParseWithinWindow(entry["within"])
}

// ParseWindowRule returns a Rule carrying only the for/within window from entry —
// the shape host watches use to reuse the rules window machinery.
func ParseWindowRule(entry map[string]any) Rule {
	forWin, withinWin := ParseWindow(entry)
	return Rule{For: forWin, Within: withinWin}
}

// ParseRuleWindow parses the global/per-service `rule_window` fallback block
// ({cycles, mode, min_matches}) into the equivalent for/within window, applied by
// ParseRules to any rule that declares neither `for` nor `within`.
// `mode: consecutive` (the default) yields a for window; `mode: within` yields
// a within window whose min_matches defaults to 1. The built-in
// default — fire on the first true cycle — is "1 consecutive", so a fallback that
// resolves to it (consecutive with cycles <= 1) returns nil/nil: it leaves rules
// at the immediate default rather than wrapping them in a redundant window.
func ParseRuleWindow(v any) (*ForWindow, *WithinWindow) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, nil
	}
	cycles, _ := cfgval.Int(m["cycles"])
	duration := cfgval.Duration(m["duration"])
	if cycles <= 0 && duration <= 0 {
		return nil, nil
	}
	switch cfgval.AsString(m["mode"]) {
	case "within":
		return nil, &WithinWindow{Cycles: cycles, Duration: duration, MinMatches: minMatches(m)}
	default: // "" or "consecutive"
		if duration > 0 {
			return &ForWindow{Duration: duration}, nil
		}
		if cycles <= 1 {
			return nil, nil
		}
		return &ForWindow{Cycles: cycles}, nil
	}
}

func minMatches(m map[string]any) int {
	matches, _ := cfgval.Int(m["min_matches"])
	if matches <= 0 {
		return 1
	}
	return matches
}
