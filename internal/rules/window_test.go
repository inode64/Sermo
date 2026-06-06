package rules

import "testing"

// feed runs a sequence of condition values through a fresh window and returns the
// cycle indexes (1-based) where the rule fired.
func feed(r Rule, values []bool) []int {
	s := &WindowState{}
	var fired []int
	for i, v := range values {
		if s.Fires(r, v) {
			fired = append(fired, i+1)
		}
	}
	return fired
}

func eqInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDefaultWindowFiresImmediately(t *testing.T) {
	// No for/within -> fire every cycle the condition is true.
	got := feed(Rule{}, []bool{true, false, true, true})
	if !eqInts(got, []int{1, 3, 4}) {
		t.Fatalf("default window fired at %v, want [1 3 4]", got)
	}
}

func TestForConsecutive(t *testing.T) {
	r := Rule{For: &ForWindow{Cycles: 3}}
	// Fires once 3 consecutive trues are seen, and keeps firing while they hold;
	// a false resets the streak.
	got := feed(r, []bool{true, true, false, true, true, true, true})
	if !eqInts(got, []int{6, 7}) {
		t.Fatalf("for-3 fired at %v, want [6 7]", got)
	}
}

func TestWithinSlidingWindow(t *testing.T) {
	r := Rule{Within: &WithinWindow{Cycles: 4, MinMatches: 2}}
	// At least 2 trues in the last 4 cycles.
	got := feed(r, []bool{true, false, false, false, true, false})
	// cycle5 window=[F,F,F,T]? no: window is last 4 => cycles 2..5 = [F,F,F,T] -> 1 match.
	// Recompute: c1[T]=1<2; c2[T,F]=1; c3[T,F,F]=1; c4[T,F,F,F]=1; c5[F,F,F,T]=1; c6[F,F,T,F]=1. none reach 2.
	if len(got) != 0 {
		t.Fatalf("within fired at %v, want none (never 2 in 4)", got)
	}

	got = feed(r, []bool{true, true, false, false, false})
	// c1=1; c2[T,T]=2 fire; c3[T,T,F]=2 fire; c4[T,T,F,F]=2 fire; c5[T,F,F,F]=1.
	if !eqInts(got, []int{2, 3, 4}) {
		t.Fatalf("within fired at %v, want [2 3 4]", got)
	}
}

func TestParseWindows(t *testing.T) {
	tree := map[string]any{"rules": map[string]any{
		"a": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"for":  map[string]any{"cycles": 3, "mode": "consecutive"},
			"then": map[string]any{"action": "restart"},
		},
		"b": map[string]any{
			"type":   "remediation",
			"if":     map[string]any{"failed": map[string]any{"check": "http"}},
			"within": map[string]any{"cycles": 15, "min_matches": 5},
			"then":   map[string]any{"action": "restart"},
		},
	}}
	ruleSet, _ := ParseRules(tree)
	byName := map[string]Rule{}
	for _, r := range ruleSet {
		byName[r.Name] = r
	}
	if byName["a"].For == nil || byName["a"].For.Cycles != 3 {
		t.Errorf("rule a For = %+v", byName["a"].For)
	}
	if byName["b"].Within == nil || byName["b"].Within.Cycles != 15 || byName["b"].Within.MinMatches != 5 {
		t.Errorf("rule b Within = %+v", byName["b"].Within)
	}
}
