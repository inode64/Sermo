package rules

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
		min := r.Within.MinMatches
		if min <= 0 {
			min = 1
		}
		return countTrue(s.history) >= min
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
