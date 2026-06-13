package rules

import (
	"fmt"
	"slices"

	"sermo/internal/cfgval"
)

// WindowState tracks a rule's condition history across cycles so for/within
// windows can be evaluated (section 15). One instance per rule per service,
// persisted by the worker between cycles.
type WindowState struct {
	consecutive int
	history     []bool // sliding window for `within`
}

// withinWindow returns a within-window's cycle count and effective minimum
// matches (defaulting to 1), and whether a within window is configured. It is the
// single source of the within defaults shared by Fires/IsFiring/Progress.
func (r Rule) withinWindow() (cycles, minMatches int, ok bool) {
	if r.Within != nil && r.Within.Cycles > 0 {
		mm := r.Within.MinMatches
		if mm <= 0 {
			mm = 1
		}
		return r.Within.Cycles, mm, true
	}
	return 0, 0, false
}

// forNeed is the number of consecutive cycles a `for` window requires; with no
// window the default is 1 (fire the moment the condition is true).
func (r Rule) forNeed() int {
	if r.For != nil && r.For.Cycles > 0 {
		return r.For.Cycles
	}
	return 1
}

// Fires updates the window with this cycle's condition value and reports whether
// the rule fires. With neither for nor within, the default is `for 1 cycle`.
func (s *WindowState) Fires(r Rule, conditionTrue bool) bool {
	if cycles, minMatches, ok := r.withinWindow(); ok {
		s.history = append(s.history, conditionTrue)
		if len(s.history) > cycles {
			s.history = s.history[len(s.history)-cycles:]
		}
		return countTrue(s.history) >= minMatches
	}
	if conditionTrue {
		s.consecutive++
	} else {
		s.consecutive = 0
	}
	return s.consecutive >= r.forNeed()
}

// counters returns the window's read-only counters; a nil state (a rule that
// has not ticked yet) reads as zero progress. The read-only methods go through
// this accessor instead of rebinding the receiver.
func (s *WindowState) counters() (consecutive int, history []bool) {
	if s == nil {
		return 0, nil
	}
	return s.consecutive, s.history
}

// IsFiring reports whether the rule would fire from the current window state
// without advancing it (read-only, nil-safe; use Fires during evaluation).
func (s *WindowState) IsFiring(r Rule) bool {
	consecutive, history := s.counters()
	if _, minMatches, ok := r.withinWindow(); ok {
		return countTrue(history) >= minMatches
	}
	return consecutive >= r.forNeed()
}

// Progress returns an operator-facing window counter such as "2/3" for
// consecutive windows or "2/3 in 15 cycles" for within windows. Nil-safe.
func (s *WindowState) Progress(r Rule) string {
	consecutive, history := s.counters()
	if cycles, minMatches, ok := r.withinWindow(); ok {
		return fmt.Sprintf("%d/%d in %d cycles", countTrue(history), minMatches, cycles)
	}
	return fmt.Sprintf("%d/%d", consecutive, r.forNeed())
}

// Clone returns a deep copy of the window state for config reload.
func (s *WindowState) Clone() *WindowState {
	if s == nil {
		return nil
	}
	cp := &WindowState{consecutive: s.consecutive}
	if len(s.history) > 0 {
		cp.history = slices.Clone(s.history)
	}
	return cp
}

// WindowDescription summarizes the configured for/within window.
func WindowDescription(r Rule) string {
	if cycles, minMatches, ok := r.withinWindow(); ok {
		return fmt.Sprintf("within %d cycles (min %d)", cycles, minMatches)
	}
	if r.For != nil && r.For.Cycles > 0 {
		return fmt.Sprintf("for %d consecutive", r.For.Cycles)
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

// ParseForWindow parses a `for` window ({cycles}) from a config node, or nil when
// absent. Shared by the rules parser and the host-watch builder.
func ParseForWindow(v any) *ForWindow {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	cycles, _ := cfgval.Int(m["cycles"])
	return &ForWindow{Cycles: cycles}
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
	matches, _ := cfgval.Int(m["min_matches"])
	if matches <= 0 {
		matches = 1
	}
	return &WithinWindow{Cycles: cycles, MinMatches: matches}
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
// ParseRules to any rule that declares neither `for` nor `within` (section 13).
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
	if cycles <= 0 {
		return nil, nil
	}
	switch cfgval.AsString(m["mode"]) {
	case "within":
		matches, _ := cfgval.Int(m["min_matches"])
		if matches <= 0 {
			matches = 1
		}
		return nil, &WithinWindow{Cycles: cycles, MinMatches: matches}
	default: // "" or "consecutive"
		if cycles <= 1 {
			return nil, nil
		}
		return &ForWindow{Cycles: cycles}, nil
	}
}
