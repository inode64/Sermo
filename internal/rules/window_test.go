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

func TestWindowProgressAndIsFiring(t *testing.T) {
	forRule := Rule{For: &ForWindow{Cycles: 3}}
	s := &WindowState{}
	if s.IsFiring(forRule) || s.Progress(forRule) != "0/3" {
		t.Fatalf("empty state: firing=%v progress=%q", s.IsFiring(forRule), s.Progress(forRule))
	}
	s.Fires(forRule, true)
	s.Fires(forRule, true)
	if s.IsFiring(forRule) || s.Progress(forRule) != "2/3" {
		t.Fatalf("after 2 trues: firing=%v progress=%q", s.IsFiring(forRule), s.Progress(forRule))
	}
	s.Fires(forRule, true)
	if !s.IsFiring(forRule) || s.Progress(forRule) != "3/3" {
		t.Fatalf("after 3 trues: firing=%v progress=%q", s.IsFiring(forRule), s.Progress(forRule))
	}

	withinRule := Rule{Within: &WithinWindow{Cycles: 4, MinMatches: 2}}
	s2 := &WindowState{}
	s2.Fires(withinRule, true)
	s2.Fires(withinRule, false)
	if s2.IsFiring(withinRule) || s2.Progress(withinRule) != "1/2 in 4 cycles" {
		t.Fatalf("within partial: firing=%v progress=%q", s2.IsFiring(withinRule), s2.Progress(withinRule))
	}
	s2.Fires(withinRule, true)
	if !s2.IsFiring(withinRule) || s2.Progress(withinRule) != "2/2 in 4 cycles" {
		t.Fatalf("within fire: firing=%v progress=%q", s2.IsFiring(withinRule), s2.Progress(withinRule))
	}
}

func TestWindowStateClone(t *testing.T) {
	r := Rule{Within: &WithinWindow{Cycles: 4, MinMatches: 2}}
	s := &WindowState{}
	s.Fires(r, true)
	s.Fires(r, false)
	cp := s.Clone()
	if cp == s || cp.Progress(r) != s.Progress(r) {
		t.Fatalf("clone progress = %q, want %q", cp.Progress(r), s.Progress(r))
	}
	s.Fires(r, true)
	if cp.Progress(r) == s.Progress(r) {
		t.Fatal("clone should not alias live state")
	}
}

func TestWindowDescription(t *testing.T) {
	if got := WindowDescription(Rule{}); got != "immediate" {
		t.Fatalf("default = %q", got)
	}
	if got := WindowDescription(Rule{For: &ForWindow{Cycles: 3}}); got != "for 3 consecutive" {
		t.Fatalf("for = %q", got)
	}
	if got := WindowDescription(Rule{Within: &WithinWindow{Cycles: 15, MinMatches: 5}}); got != "within 15 cycles (min 5)" {
		t.Fatalf("within = %q", got)
	}
}

func TestParseRuleWindow(t *testing.T) {
	cases := []struct {
		name       string
		in         any
		wantFor    int // 0 = nil ForWindow
		wantWithin [2]int
	}{
		{"absent", nil, 0, [2]int{}},
		{"not a map", "x", 0, [2]int{}},
		{"default 1 consecutive is a no-op", map[string]any{"cycles": 1, "mode": "consecutive"}, 0, [2]int{}},
		{"implicit mode defaults to consecutive", map[string]any{"cycles": 1}, 0, [2]int{}},
		{"zero cycles", map[string]any{"cycles": 0}, 0, [2]int{}},
		{"consecutive N", map[string]any{"cycles": 3, "mode": "consecutive"}, 3, [2]int{}},
		{"within with min_matches", map[string]any{"cycles": 15, "mode": "within", "min_matches": 5}, 0, [2]int{15, 5}},
		{"within defaults min_matches to 1", map[string]any{"cycles": 10, "mode": "sliding"}, 0, [2]int{10, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forWin, withinWin := ParseRuleWindow(tc.in)
			gotFor := 0
			if forWin != nil {
				gotFor = forWin.Cycles
			}
			if gotFor != tc.wantFor {
				t.Errorf("For = %v, want cycles %d", forWin, tc.wantFor)
			}
			var gotWithin [2]int
			if withinWin != nil {
				gotWithin = [2]int{withinWin.Cycles, withinWin.MinMatches}
			}
			if gotWithin != tc.wantWithin {
				t.Errorf("Within = %v, want %v", withinWin, tc.wantWithin)
			}
		})
	}
}

func TestParseRulesAppliesWindowFallback(t *testing.T) {
	tree := map[string]any{
		"rule_window": map[string]any{"cycles": 3, "mode": "consecutive"},
		"rules": map[string]any{
			// inherits the fallback (declares neither for nor within)
			"inherit": map[string]any{
				"type": "remediation",
				"if":   map[string]any{"failed": map[string]any{"check": "http"}},
				"then": map[string]any{"action": "restart"},
			},
			// its own `for` wins over the fallback
			"ownfor": map[string]any{
				"type": "remediation",
				"if":   map[string]any{"failed": map[string]any{"check": "http"}},
				"for":  map[string]any{"cycles": 2},
				"then": map[string]any{"action": "restart"},
			},
			// its own `within` wins over the fallback
			"ownwithin": map[string]any{
				"type":   "remediation",
				"if":     map[string]any{"failed": map[string]any{"check": "http"}},
				"within": map[string]any{"cycles": 5, "min_matches": 2},
				"then":   map[string]any{"action": "restart"},
			},
		},
	}
	ruleSet, _ := ParseRules(tree)
	byName := map[string]Rule{}
	for _, r := range ruleSet {
		byName[r.Name] = r
	}
	if r := byName["inherit"]; r.For == nil || r.For.Cycles != 3 || r.Within != nil {
		t.Errorf("inherit: For=%+v Within=%+v, want For cycles 3", r.For, r.Within)
	}
	if r := byName["ownfor"]; r.For == nil || r.For.Cycles != 2 {
		t.Errorf("ownfor: For=%+v, want cycles 2 (own window, not fallback)", r.For)
	}
	if r := byName["ownwithin"]; r.Within == nil || r.Within.Cycles != 5 || r.For != nil {
		t.Errorf("ownwithin: For=%+v Within=%+v, want Within cycles 5", r.For, r.Within)
	}
}

func TestParseRulesNoFallbackKeepsImmediate(t *testing.T) {
	tree := map[string]any{
		"rules": map[string]any{
			"a": map[string]any{
				"type": "remediation",
				"if":   map[string]any{"failed": map[string]any{"check": "http"}},
				"then": map[string]any{"action": "restart"},
			},
		},
	}
	ruleSet, _ := ParseRules(tree)
	if len(ruleSet) != 1 {
		t.Fatalf("got %d rules", len(ruleSet))
	}
	if r := ruleSet[0]; r.For != nil || r.Within != nil {
		t.Errorf("expected immediate default (no window), got For=%+v Within=%+v", r.For, r.Within)
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
