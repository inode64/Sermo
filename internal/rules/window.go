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
// for/within windows can be evaluated, plus the firing episode those windows
// open and the progress of an optional `clear` window that ends it. One
// instance per rule per service, persisted by the worker between cycles.
type WindowState struct {
	consecutive  int
	history      []bool // sliding window for `within: {cycles: ...}`
	trueSince    time.Time
	timedHistory []WindowSample // true samples for `within: {duration: ...}`
	// firing is the current episode: it rises when the entry window matures and
	// falls when the entry window stops firing (immediately, or after the clear
	// window when one is configured). Reading it before FiresAt is the only
	// race-free way to observe the rising/falling edge: recomputing IsFiringAt
	// with the same timestamp reads a duration window as already elapsed.
	firing           bool
	clearConsecutive int
	clearSince       time.Time
}

// WindowStateSnapshot is the serializable form of a WindowState.
type WindowStateSnapshot struct {
	Consecutive      int
	History          []bool
	TrueSince        time.Time
	TimedHistory     []WindowSample
	Firing           bool
	ClearConsecutive int
	ClearSince       time.Time
}

// withinWindow returns a within-window's cycle count and effective minimum
// matches (defaulting to 1), and whether a within window is configured. It is the
// single source of the within defaults shared by FiresAt/IsFiringAt/ProgressAt.
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

// clearWindow returns the configured clear window: the consecutive false cycles
// or wall-clock duration the condition must stay clear before a firing episode
// ends. Both zero means no clear window (the episode ends the first cycle the
// entry window stops firing).
func (r Rule) clearWindow() (cycles int, duration time.Duration) {
	if r.Clear != nil {
		if r.Clear.Duration > 0 {
			return 0, r.Clear.Duration
		}
		if r.Clear.Cycles > 0 {
			return r.Clear.Cycles, 0
		}
	}
	return 0, 0
}

// FiresAt updates the window with this cycle's condition value and reports
// whether the rule is in a firing episode. The entry window (for/within;
// neither defaults to `for 1 cycle`) opens the episode; without a clear window
// the episode tracks the entry window exactly, and with one the episode is held
// open until the condition stays false for the whole clear window (a clear
// window only extends an episode, it never cuts the entry window short).
// Workers and watches pass an explicit observation time so duration-based
// windows share the same injected clock as policy/cooldown logic and tests can
// avoid wall-clock sleeps.
func (s *WindowState) FiresAt(r Rule, conditionTrue bool, at time.Time) bool {
	raw := s.advance(r, conditionTrue, at)
	clearCycles, clearDuration := r.clearWindow()
	if (clearCycles == 0 && clearDuration == 0) || !s.firing {
		s.firing = raw
		s.resetClear()
		return s.firing
	}
	if conditionTrue {
		// The episode continues; the entry window may be rebuilding after a dip,
		// so the raw value is not consulted until the condition clears again.
		s.resetClear()
		return true
	}
	if s.clearSince.IsZero() {
		s.clearSince = at
	}
	s.clearConsecutive++
	cleared := s.clearConsecutive >= clearCycles
	if clearDuration > 0 {
		cleared = durationElapsed(s.clearSince, at) >= clearDuration
	}
	s.firing = raw || !cleared
	if !s.firing {
		s.resetClear()
	}
	return s.firing
}

// advance updates the entry window (for/within) with this cycle's condition
// value and reports whether it fires, independent of the episode/clear state.
func (s *WindowState) advance(r Rule, conditionTrue bool, at time.Time) bool {
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

// resetClear drops the clear window's progress, on episode boundaries and every
// cycle the condition is true again.
func (s *WindowState) resetClear() {
	s.clearConsecutive = 0
	s.clearSince = time.Time{}
}

// Firing reports whether the rule is currently in a firing episode, as of the
// last FiresAt call (read-only, nil-safe). Callers observing the rising or
// falling edge must read it before calling FiresAt for the cycle.
func (s *WindowState) Firing() bool {
	return s != nil && s.firing
}

// EndEpisode force-closes the firing episode without advancing the entry
// window, used when a restored episode is reconciled against a check that no
// longer fires.
func (s *WindowState) EndEpisode() {
	if s == nil {
		return
	}
	s.firing = false
	s.resetClear()
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

// IsFiringAt reports whether the rule is firing from the current window state
// without advancing it (read-only, nil-safe; use FiresAt during evaluation).
// An open episode — including one held by a clear window — reads as firing;
// otherwise the entry window's counters decide. at is the read time for
// duration windows.
func (s *WindowState) IsFiringAt(r Rule, at time.Time) bool {
	if s.Firing() {
		return true
	}
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

// ProgressAt returns an operator-facing window counter such as "2/3" for
// consecutive windows, "2m/6m" for duration windows, or "2/3 in 15 cycles" for
// within windows. Nil-safe. at is the read time for duration windows.
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
		Consecutive:      s.consecutive,
		History:          slices.Clone(s.history),
		TrueSince:        s.trueSince,
		TimedHistory:     slices.Clone(s.timedHistory),
		Firing:           s.firing,
		ClearConsecutive: s.clearConsecutive,
		ClearSince:       s.clearSince,
	}
}

// WindowStateFromSnapshot restores a window state snapshot.
func WindowStateFromSnapshot(snapshot WindowStateSnapshot) *WindowState {
	snapshot.Consecutive = max(snapshot.Consecutive, 0)
	return &WindowState{
		consecutive:      snapshot.Consecutive,
		history:          slices.Clone(snapshot.History),
		trueSince:        snapshot.TrueSince,
		timedHistory:     slices.Clone(snapshot.TimedHistory),
		firing:           snapshot.Firing,
		clearConsecutive: max(snapshot.ClearConsecutive, 0),
		clearSince:       snapshot.ClearSince,
	}
}

// WindowDescription summarizes the configured for/within window.
func WindowDescription(r Rule) string {
	window := describeWindow(r)
	if window.within {
		if window.duration > 0 {
			return fmt.Sprintf("within %s (min %d)", formatWindowDuration(window.duration), window.minMatches)
		}
		return fmt.Sprintf("within %d cycles (min %d)", window.cycles, window.minMatches)
	}
	if window.duration > 0 {
		return "for " + formatWindowDuration(window.duration)
	}
	if window.cycles > 0 {
		return fmt.Sprintf("for %d consecutive", window.cycles)
	}
	return "immediate"
}

type windowDescription struct {
	within     bool
	cycles     int
	duration   time.Duration
	minMatches int
}

func describeWindow(r Rule) windowDescription {
	if cycles, duration, minMatches, ok := r.withinWindow(); ok {
		return windowDescription{within: true, cycles: cycles, duration: duration, minMatches: minMatches}
	}
	if r.For != nil {
		return windowDescription{cycles: r.For.Cycles, duration: r.For.Duration}
	}
	return windowDescription{}
}

// WindowDurationDescription summarizes only the rule's configured time/span,
// suitable for short alert templates. It returns "current cycle" for immediate
// rules that have no explicit for/within window.
func WindowDurationDescription(r Rule) string {
	window := describeWindow(r)
	if window.duration > 0 {
		return formatWindowDuration(window.duration)
	}
	if window.cycles > 0 {
		return fmt.Sprintf("%d cycles", window.cycles)
	}
	return "current cycle"
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
	cycles, _ := cfgval.Int(m[WindowKeyCycles])
	return &ForWindow{Cycles: cycles, Duration: cfgval.Duration(m[WindowKeyDuration])}
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
	cycles, _ := cfgval.Int(m[WindowKeyCycles])
	return &WithinWindow{Cycles: cycles, Duration: cfgval.Duration(m[WindowKeyDuration]), MinMatches: minMatches(m)}
}

// DefaultClearWindow is the built-in recovery hysteresis: when neither the
// rule/watch nor a global/per-service `clear_window` block configures a clear
// window, alert rules and watches hold their firing episode until the
// condition has stayed clear this long. `clear: {cycles: 1}` opts a target
// back into immediate clearing.
const DefaultClearWindow = 5 * time.Minute

// ParseClearWindow parses a `clear` window ({cycles} or {duration}) from a
// config node, or nil when absent. It shares ForWindow's shape: the consecutive
// false cycles or wall-clock duration the condition must stay clear before a
// firing episode ends.
func ParseClearWindow(v any) *ForWindow {
	return ParseForWindow(v)
}

// ClearWindowOrDefault parses a `clear_window` fallback block, substituting the
// built-in DefaultClearWindow when the block is absent or not a mapping. Shared
// by ParseRules and the host-watch builder so both surfaces inherit the same
// default.
func ClearWindowOrDefault(v any) *ForWindow {
	if w := ParseClearWindow(v); w != nil {
		return w
	}
	return &ForWindow{Duration: DefaultClearWindow}
}

// ParseWindow parses an entry's `for`/`within` sub-blocks into their windows.
// Shared by the rules parser and the host-watch builder so both read a window the
// same way.
func ParseWindow(entry map[string]any) (*ForWindow, *WithinWindow) {
	return ParseForWindow(entry[RuleFieldFor]), ParseWithinWindow(entry[RuleFieldWithin])
}

// ParseWindowRule returns a Rule carrying only the for/within/clear windows from
// entry — the shape host watches use to reuse the rules window machinery.
func ParseWindowRule(entry map[string]any) Rule {
	forWin, withinWin := ParseWindow(entry)
	return Rule{For: forWin, Within: withinWin, Clear: ParseClearWindow(entry[RuleFieldClear])}
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
	cycles, _ := cfgval.Int(m[WindowKeyCycles])
	duration := cfgval.Duration(m[WindowKeyDuration])
	if cycles <= 0 && duration <= 0 {
		return nil, nil
	}
	switch cfgval.AsString(m[FieldMode]) {
	case WindowModeWithin:
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
	matches, _ := cfgval.Int(m[WindowKeyMinMatches])
	if matches <= 0 {
		return 1
	}
	return matches
}
