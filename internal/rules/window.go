package rules

import "fmt"

// WindowState tracks a rule's condition history across cycles so for/within
// windows can be evaluated (section 15). One instance per rule per service,
// persisted by the worker between cycles.
type WindowState struct {
	consecutive int
	history     []bool // sliding window for `within`
}

// Fires updates the window with this cycle's condition value and reports whether
// the rule fires. With neither for nor within, the default is `for 1 cycle`
// (fire the moment the condition is true).
func (s *WindowState) Fires(r Rule, conditionTrue bool) bool {
	if r.Within != nil && r.Within.Cycles > 0 {
		s.history = append(s.history, conditionTrue)
		if len(s.history) > r.Within.Cycles {
			s.history = s.history[len(s.history)-r.Within.Cycles:]
		}
		minMatches := r.Within.MinMatches
		if minMatches <= 0 {
			minMatches = 1
		}
		return countTrue(s.history) >= minMatches
	}

	// `for` (consecutive); default is 1 cycle.
	need := 1
	if r.For != nil && r.For.Cycles > 0 {
		need = r.For.Cycles
	}
	if conditionTrue {
		s.consecutive++
	} else {
		s.consecutive = 0
	}
	return s.consecutive >= need
}

// IsFiring reports whether the rule would fire from the current window state
// without advancing it (read-only; use Fires during evaluation).
func (s *WindowState) IsFiring(r Rule) bool {
	if s == nil {
		s = &WindowState{}
	}
	if r.Within != nil && r.Within.Cycles > 0 {
		minMatches := r.Within.MinMatches
		if minMatches <= 0 {
			minMatches = 1
		}
		return countTrue(s.history) >= minMatches
	}
	need := 1
	if r.For != nil && r.For.Cycles > 0 {
		need = r.For.Cycles
	}
	return s.consecutive >= need
}

// Progress returns an operator-facing window counter such as "2/3" for
// consecutive windows or "2/3 in 15 cycles" for within windows.
func (s *WindowState) Progress(r Rule) string {
	if s == nil {
		s = &WindowState{}
	}
	if r.Within != nil && r.Within.Cycles > 0 {
		minMatches := r.Within.MinMatches
		if minMatches <= 0 {
			minMatches = 1
		}
		return fmt.Sprintf("%d/%d in %d cycles", countTrue(s.history), minMatches, r.Within.Cycles)
	}
	need := 1
	if r.For != nil && r.For.Cycles > 0 {
		need = r.For.Cycles
	}
	return fmt.Sprintf("%d/%d", s.consecutive, need)
}

// Clone returns a deep copy of the window state for config reload.
func (s *WindowState) Clone() *WindowState {
	if s == nil {
		return nil
	}
	cp := &WindowState{consecutive: s.consecutive}
	if len(s.history) > 0 {
		cp.history = append([]bool(nil), s.history...)
	}
	return cp
}

// WindowDescription summarizes the configured for/within window.
func WindowDescription(r Rule) string {
	if r.Within != nil && r.Within.Cycles > 0 {
		minMatches := r.Within.MinMatches
		if minMatches <= 0 {
			minMatches = 1
		}
		return fmt.Sprintf("within %d cycles (min %d)", r.Within.Cycles, minMatches)
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

func parseForWindow(v any) *ForWindow {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	cycles, _ := parseInt(m["cycles"])
	return &ForWindow{Cycles: cycles, Mode: asString(m["mode"])}
}

func parseWithinWindow(v any) *WithinWindow {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	cycles, _ := parseInt(m["cycles"])
	matches, _ := parseInt(m["min_matches"])
	return &WithinWindow{Cycles: cycles, MinMatches: matches}
}
