package rules

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// parseRulesByName parses tree's rules and returns them indexed by rule name.
func parseRulesByName(t *testing.T, tree map[string]any) map[string]Rule {
	t.Helper()
	ruleSet, _ := ParseRules(tree)
	byName := map[string]Rule{}
	for _, r := range ruleSet {
		byName[r.Name] = r
	}
	return byName
}

// feed runs a sequence of condition values through a fresh window and returns the
// cycle indexes (1-based) where the rule fired.
func feed(r Rule, values []bool) []int {
	s := &WindowState{}
	var fired []int
	for i, v := range values {
		if s.FiresAt(r, v, time.Now()) {
			fired = append(fired, i+1)
		}
	}
	return fired
}

func TestDefaultWindowFiresImmediately(t *testing.T) {
	// No for/within -> fire every cycle the condition is true.
	got := feed(Rule{}, []bool{true, false, true, true})
	if !slices.Equal(got, []int{1, 3, 4}) {
		t.Fatalf("default window fired at %v, want [1 3 4]", got)
	}
}

func TestForConsecutive(t *testing.T) {
	r := Rule{For: &ForWindow{Cycles: 3}}
	// Fires once 3 consecutive trues are seen, and keeps firing while they hold;
	// a false resets the streak.
	got := feed(r, []bool{true, true, false, true, true, true, true})
	if !slices.Equal(got, []int{6, 7}) {
		t.Fatalf("for-3 fired at %v, want [6 7]", got)
	}
}

func TestForDuration(t *testing.T) {
	at := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	r := Rule{For: &ForWindow{Duration: 6 * time.Minute}}
	s := &WindowState{}
	if s.FiresAt(r, true, at) {
		t.Fatal("first true sample must not satisfy a duration window")
	}
	if got := s.ProgressAt(r, at.Add(3*time.Minute)); got != "3m/6m" {
		t.Fatalf("progress after 3m = %q, want 3m/6m", got)
	}
	if s.FiresAt(r, true, at.Add(5*time.Minute)) {
		t.Fatal("duration window fired before 6m")
	}
	if !s.FiresAt(r, true, at.Add(6*time.Minute)) {
		t.Fatal("duration window did not fire at 6m")
	}
	if got := s.ProgressAt(r, at.Add(7*time.Minute)); got != "6m/6m" {
		t.Fatalf("progress after firing = %q, want capped 6m/6m", got)
	}
	s.FiresAt(r, false, at.Add(8*time.Minute))
	if got := s.ProgressAt(r, at.Add(9*time.Minute)); got != "0s/6m" {
		t.Fatalf("progress after reset = %q, want 0s/6m", got)
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
	if !slices.Equal(got, []int{2, 3, 4}) {
		t.Fatalf("within fired at %v, want [2 3 4]", got)
	}
}

func TestWithinDurationWindow(t *testing.T) {
	at := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	r := Rule{Within: &WithinWindow{Duration: 10 * time.Minute, MinMatches: 2}}
	s := &WindowState{}
	if s.FiresAt(r, true, at) {
		t.Fatal("one match is not enough")
	}
	if !s.FiresAt(r, true, at.Add(9*time.Minute)) {
		t.Fatal("two matches inside the duration window should fire")
	}
	if s.FiresAt(r, false, at.Add(21*time.Minute)) {
		t.Fatal("old matches outside the duration window must expire")
	}
	if got := s.ProgressAt(r, at.Add(21*time.Minute)); got != "0/2 in 10m" {
		t.Fatalf("progress after expiry = %q, want 0/2 in 10m", got)
	}
}

func TestWindowProgressAndIsFiring(t *testing.T) {
	forRule := Rule{For: &ForWindow{Cycles: 3}}
	s := &WindowState{}
	if s.IsFiringAt(forRule, time.Now()) || s.ProgressAt(forRule, time.Now()) != "0/3" {
		t.Fatalf("empty state: firing=%v progress=%q", s.IsFiringAt(forRule, time.Now()), s.ProgressAt(forRule, time.Now()))
	}
	s.FiresAt(forRule, true, time.Now())
	s.FiresAt(forRule, true, time.Now())
	if s.IsFiringAt(forRule, time.Now()) || s.ProgressAt(forRule, time.Now()) != "2/3" {
		t.Fatalf("after 2 trues: firing=%v progress=%q", s.IsFiringAt(forRule, time.Now()), s.ProgressAt(forRule, time.Now()))
	}
	s.FiresAt(forRule, true, time.Now())
	if !s.IsFiringAt(forRule, time.Now()) || s.ProgressAt(forRule, time.Now()) != "3/3" {
		t.Fatalf("after 3 trues: firing=%v progress=%q", s.IsFiringAt(forRule, time.Now()), s.ProgressAt(forRule, time.Now()))
	}

	withinRule := Rule{Within: &WithinWindow{Cycles: 4, MinMatches: 2}}
	s2 := &WindowState{}
	s2.FiresAt(withinRule, true, time.Now())
	s2.FiresAt(withinRule, false, time.Now())
	if s2.IsFiringAt(withinRule, time.Now()) || s2.ProgressAt(withinRule, time.Now()) != "1/2 in 4 cycles" {
		t.Fatalf("within partial: firing=%v progress=%q", s2.IsFiringAt(withinRule, time.Now()), s2.ProgressAt(withinRule, time.Now()))
	}
	s2.FiresAt(withinRule, true, time.Now())
	if !s2.IsFiringAt(withinRule, time.Now()) || s2.ProgressAt(withinRule, time.Now()) != "2/2 in 4 cycles" {
		t.Fatalf("within fire: firing=%v progress=%q", s2.IsFiringAt(withinRule, time.Now()), s2.ProgressAt(withinRule, time.Now()))
	}
}

func TestForDurationEpisodeRisingEdge(t *testing.T) {
	at := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	r := Rule{For: &ForWindow{Duration: 6 * time.Minute}}
	s := &WindowState{}
	if s.Firing() {
		t.Fatal("a fresh window must not report a firing episode")
	}
	s.FiresAt(r, true, at)
	if s.Firing() {
		t.Fatal("must not be in a firing episode before the duration elapses")
	}
	wasFiring := s.Firing()
	if !s.FiresAt(r, true, at.Add(6*time.Minute)) || wasFiring {
		t.Fatal("the rising edge of a duration window must be observable: Firing() false before FiresAt matures")
	}
	if !s.Firing() {
		t.Fatal("Firing() must report the episode after the duration window matures")
	}
	s.FiresAt(r, false, at.Add(7*time.Minute))
	if s.Firing() {
		t.Fatal("without a clear window the episode must end on the first false cycle")
	}
}

func TestClearWindowCycles(t *testing.T) {
	r := Rule{Clear: &ForWindow{Cycles: 2}}
	// c1 fires; c2 false is held (1/2); c3 true continues the same episode and
	// resets the clear streak; c4 false held (1/2); c5 false clears (2/2).
	got := feed(r, []bool{true, false, true, false, false, false})
	if !slices.Equal(got, []int{1, 2, 3, 4}) {
		t.Fatalf("clear-2 fired at %v, want [1 2 3 4]", got)
	}
}

func TestClearWindowDuration(t *testing.T) {
	at := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	r := Rule{Clear: &ForWindow{Duration: 4 * time.Minute}}
	s := &WindowState{}
	if !s.FiresAt(r, true, at) {
		t.Fatal("immediate rule must fire on the first true cycle")
	}
	if !s.FiresAt(r, false, at.Add(1*time.Minute)) {
		t.Fatal("clear duration must hold the episode on the first false cycle")
	}
	if !s.FiresAt(r, false, at.Add(3*time.Minute)) {
		t.Fatal("clear duration must hold the episode before it elapses")
	}
	if s.FiresAt(r, false, at.Add(5*time.Minute)) {
		t.Fatal("episode must end once the condition stayed false for the clear duration")
	}
	if s.Firing() {
		t.Fatal("Firing() must be false after the clear window ends the episode")
	}
}

func TestClearWindowNeverTruncatesEntryWindow(t *testing.T) {
	// A within window may keep matching after the condition drops; a clear window
	// only extends an episode, it never cuts one short.
	r := Rule{Within: &WithinWindow{Cycles: 4, MinMatches: 2}, Clear: &ForWindow{Cycles: 1}}
	got := feed(r, []bool{true, true, false, false, false})
	if !slices.Equal(got, []int{2, 3, 4}) {
		t.Fatalf("within+clear fired at %v, want [2 3 4]", got)
	}
}

func TestParseRulesClearWindow(t *testing.T) {
	tree := map[string]any{"rules": map[string]any{
		"alert-high": map[string]any{
			"type":  "alert",
			"if":    map[string]any{"failed": map[string]any{"check": "http"}},
			"then":  map[string]any{"action": "alert", "message": "high"},
			"clear": map[string]any{"cycles": 2},
		},
		"restart-down": map[string]any{
			"type":  "remediation",
			"if":    map[string]any{"failed": map[string]any{"check": "http"}},
			"then":  map[string]any{"action": "restart"},
			"clear": map[string]any{"cycles": 2},
		},
	}}
	ruleSet, warnings := ParseRules(tree)
	byName := map[string]Rule{}
	for _, r := range ruleSet {
		byName[r.Name] = r
	}
	if a := byName["alert-high"]; a.Clear == nil || a.Clear.Cycles != 2 {
		t.Fatalf("alert rule clear = %+v, want cycles 2", a.Clear)
	}
	if r := byName["restart-down"]; r.Clear != nil {
		t.Fatalf("remediation rule must drop its clear window, got %+v", r.Clear)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "clear") {
		t.Fatalf("expected one clear warning, got %v", warnings)
	}
}

func TestWindowStateSnapshotKeepsEpisode(t *testing.T) {
	at := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	r := Rule{Clear: &ForWindow{Cycles: 3}}
	s := &WindowState{}
	s.FiresAt(r, true, at)
	s.FiresAt(r, false, at.Add(time.Minute)) // held 1/3
	restored := WindowStateFromSnapshot(s.Snapshot())
	if !restored.Firing() {
		t.Fatal("snapshot restore must keep the firing episode")
	}
	if !restored.FiresAt(r, false, at.Add(2*time.Minute)) {
		t.Fatal("restored clear progress must keep holding the episode (2/3)")
	}
	if restored.FiresAt(r, false, at.Add(3*time.Minute)) {
		t.Fatal("restored episode must clear after the remaining false cycles (3/3)")
	}
}

func TestWindowStateClone(t *testing.T) {
	r := Rule{Within: &WithinWindow{Cycles: 4, MinMatches: 2}}
	s := &WindowState{}
	s.FiresAt(r, true, time.Now())
	s.FiresAt(r, false, time.Now())
	cp := s.Clone()
	if cp == s || cp.ProgressAt(r, time.Now()) != s.ProgressAt(r, time.Now()) {
		t.Fatalf("clone progress = %q, want %q", cp.ProgressAt(r, time.Now()), s.ProgressAt(r, time.Now()))
	}
	s.FiresAt(r, true, time.Now())
	if cp.ProgressAt(r, time.Now()) == s.ProgressAt(r, time.Now()) {
		t.Fatal("clone should not alias live state")
	}
	// clone must be independent even for history used by within-window
	// "min matches" logic (behavior sensitive for rule evaluation across reloads).
}

func TestWindowStateSnapshotRoundTrip(t *testing.T) {
	forRule := Rule{For: &ForWindow{Cycles: 3}}
	s := &WindowState{}
	s.FiresAt(forRule, true, time.Now())
	s.FiresAt(forRule, true, time.Now())
	restored := WindowStateFromSnapshot(s.Snapshot())
	if restored.ProgressAt(forRule, time.Now()) != "2/3" {
		t.Fatalf("restored for progress = %q, want 2/3", restored.ProgressAt(forRule, time.Now()))
	}

	withinRule := Rule{Within: &WithinWindow{Cycles: 4, MinMatches: 2}}
	w := &WindowState{}
	w.FiresAt(withinRule, true, time.Now())
	w.FiresAt(withinRule, false, time.Now())
	snapshot := w.Snapshot()
	restored = WindowStateFromSnapshot(snapshot)
	w.FiresAt(withinRule, true, time.Now())
	if restored.ProgressAt(withinRule, time.Now()) != "1/2 in 4 cycles" {
		t.Fatalf("restored within progress = %q, want 1/2 in 4 cycles", restored.ProgressAt(withinRule, time.Now()))
	}
	if restored.ProgressAt(withinRule, time.Now()) == w.ProgressAt(withinRule, time.Now()) {
		t.Fatal("snapshot restore should not alias live history")
	}

	at := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	forDuration := Rule{For: &ForWindow{Duration: 6 * time.Minute}}
	d := &WindowState{}
	d.FiresAt(forDuration, true, at)
	restored = WindowStateFromSnapshot(d.Snapshot())
	if got := restored.ProgressAt(forDuration, at.Add(3*time.Minute)); got != "3m/6m" {
		t.Fatalf("restored for-duration progress = %q, want 3m/6m", got)
	}

	withinDuration := Rule{Within: &WithinWindow{Duration: 10 * time.Minute, MinMatches: 2}}
	td := &WindowState{}
	td.FiresAt(withinDuration, true, at)
	td.FiresAt(withinDuration, false, at.Add(time.Minute))
	restored = WindowStateFromSnapshot(td.Snapshot())
	td.FiresAt(withinDuration, true, at.Add(2*time.Minute))
	if got := restored.ProgressAt(withinDuration, at.Add(2*time.Minute)); got != "1/2 in 10m" {
		t.Fatalf("restored within-duration progress = %q, want 1/2 in 10m", got)
	}
	if restored.ProgressAt(withinDuration, at.Add(2*time.Minute)) == td.ProgressAt(withinDuration, at.Add(2*time.Minute)) {
		t.Fatal("duration snapshot restore should not alias live history")
	}
}

func TestWindowDescription(t *testing.T) {
	if got := WindowDescription(Rule{}); got != "immediate" {
		t.Fatalf("default = %q", got)
	}
	if got := WindowDescription(Rule{For: &ForWindow{Cycles: 3}}); got != "for 3 consecutive" {
		t.Fatalf("for = %q", got)
	}
	// A For window with no positive cycles or duration degrades to immediate.
	if got := WindowDescription(Rule{For: &ForWindow{}}); got != "immediate" {
		t.Fatalf("empty For = %q, want immediate", got)
	}
	if got := WindowDescription(Rule{Within: &WithinWindow{Cycles: 15, MinMatches: 5}}); got != "within 15 cycles (min 5)" {
		t.Fatalf("within = %q", got)
	}
	if got := WindowDescription(Rule{For: &ForWindow{Duration: 6 * time.Minute}}); got != "for 6m" {
		t.Fatalf("for duration = %q", got)
	}
	if got := WindowDescription(Rule{Within: &WithinWindow{Duration: 30 * time.Minute, MinMatches: 3}}); got != "within 30m (min 3)" {
		t.Fatalf("within duration = %q", got)
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
		{"consecutive duration", map[string]any{"duration": "6m", "mode": "consecutive"}, -6, [2]int{}},
		{"within with min_matches", map[string]any{"cycles": 15, "mode": "within", "min_matches": 5}, 0, [2]int{15, 5}},
		{"within defaults min_matches to 1", map[string]any{"cycles": 10, "mode": "within"}, 0, [2]int{10, 1}},
		{"within duration", map[string]any{"duration": "30m", "mode": "within", "min_matches": 3}, 0, [2]int{-30, 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forWin, withinWin := ParseRuleWindow(tc.in)
			gotFor := 0
			if forWin != nil {
				gotFor = forWin.Cycles
				if forWin.Duration > 0 {
					gotFor = -int(forWin.Duration / time.Minute)
				}
			}
			if gotFor != tc.wantFor {
				t.Errorf("For = %v, want cycles %d", forWin, tc.wantFor)
			}
			var gotWithin [2]int
			if withinWin != nil {
				gotWithin = [2]int{withinWin.Cycles, withinWin.MinMatches}
				if withinWin.Duration > 0 {
					gotWithin[0] = -int(withinWin.Duration / time.Minute)
				}
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
	byName := parseRulesByName(t, tree)
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
			"for":  map[string]any{"cycles": 3},
			"then": map[string]any{"action": "restart"},
		},
		"b": map[string]any{
			"type":   "remediation",
			"if":     map[string]any{"failed": map[string]any{"check": "http"}},
			"within": map[string]any{"cycles": 15, "min_matches": 5},
			"then":   map[string]any{"action": "restart"},
		},
	}}
	byName := parseRulesByName(t, tree)
	if byName["a"].For == nil || byName["a"].For.Cycles != 3 {
		t.Errorf("rule a For = %+v", byName["a"].For)
	}
	if byName["b"].Within == nil || byName["b"].Within.Cycles != 15 || byName["b"].Within.MinMatches != 5 {
		t.Errorf("rule b Within = %+v", byName["b"].Within)
	}
}

func TestParseDurationWindows(t *testing.T) {
	tree := map[string]any{"rules": map[string]any{
		"a": map[string]any{
			"type": "remediation",
			"if":   map[string]any{"failed": map[string]any{"check": "http"}},
			"for":  map[string]any{"duration": "6m"},
			"then": map[string]any{"action": "restart"},
		},
		"b": map[string]any{
			"type":   "alert",
			"if":     map[string]any{"failed": map[string]any{"check": "http"}},
			"within": map[string]any{"duration": "30m", "min_matches": 3},
			"then":   map[string]any{"action": "alert", "message": "http down"},
		},
	}}
	byName := parseRulesByName(t, tree)
	if byName["a"].For == nil || byName["a"].For.Duration != 6*time.Minute {
		t.Fatalf("rule a For = %+v", byName["a"].For)
	}
	if byName["b"].Within == nil || byName["b"].Within.Duration != 30*time.Minute || byName["b"].Within.MinMatches != 3 {
		t.Fatalf("rule b Within = %+v", byName["b"].Within)
	}
}

func TestParseWithinWindowDefaultsMinMatches(t *testing.T) {
	w := ParseWithinWindow(map[string]any{"cycles": 5})
	if w == nil || w.Cycles != 5 || w.MinMatches != 1 {
		t.Fatalf("ParseWithinWindow({cycles:5}) = %+v, want {5 1}", w)
	}
}

// TestWindowStateNilReceiver locks the nil-state semantics the rule-window
// view relies on (a rule that has not ticked yet has no WindowState): the
// read-only methods must not panic and must read as zero progress, for both
// window kinds. Fires intentionally requires a non-nil state — it mutates.
func TestWindowStateNilReceiver(t *testing.T) {
	var s *WindowState

	forRule := Rule{For: &ForWindow{Cycles: 3}}
	if s.IsFiringAt(forRule, time.Now()) {
		t.Fatal("nil state must not read as firing")
	}
	if got := s.ProgressAt(forRule, time.Now()); got != "0/3" {
		t.Fatalf("Progress = %q, want 0/3", got)
	}

	withinRule := Rule{Within: &WithinWindow{Cycles: 15, MinMatches: 5}}
	if s.IsFiringAt(withinRule, time.Now()) {
		t.Fatal("nil state must not read as firing (within)")
	}
	if got := s.ProgressAt(withinRule, time.Now()); got != "0/5 in 15 cycles" {
		t.Fatalf("Progress = %q, want 0/5 in 15 cycles", got)
	}

	forDurationRule := Rule{For: &ForWindow{Duration: time.Minute}}
	if s.IsFiringAt(forDurationRule, time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)) {
		t.Fatal("nil state must not read as firing (for duration)")
	}
	if got := s.ProgressAt(forDurationRule, time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)); got != "0s/1m" {
		t.Fatalf("Progress = %q, want 0s/1m", got)
	}
}

func TestFormatWindowDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                       "0s",
		2 * time.Hour:           "2h",
		90 * time.Minute:        "90m",
		45 * time.Second:        "45s",
		1500 * time.Millisecond: "1.5s",
	}
	for d, want := range cases {
		if got := formatWindowDuration(d); got != want {
			t.Errorf("formatWindowDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestWindowDurationDescription(t *testing.T) {
	cases := []struct {
		name string
		rule Rule
		want string
	}{
		{name: "immediate", rule: Rule{}, want: "current cycle"},
		{name: "for duration", rule: Rule{For: &ForWindow{Duration: 10 * time.Minute}}, want: "10m"},
		{name: "for cycles", rule: Rule{For: &ForWindow{Cycles: 3}}, want: "3 cycles"},
		{name: "within duration", rule: Rule{Within: &WithinWindow{Duration: time.Hour, MinMatches: 2}}, want: "1h"},
		{name: "within cycles", rule: Rule{Within: &WithinWindow{Cycles: 5, MinMatches: 2}}, want: "5 cycles"},
	}
	for _, tt := range cases {
		if got := WindowDurationDescription(tt.rule); got != tt.want {
			t.Errorf("%s: WindowDurationDescription = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestParseRuleWindowZeroIsInert(t *testing.T) {
	// cycles 0 and duration 0 means "no window" even with mode: within — both
	// thresholds must be non-positive to bail out.
	if fw, ww := ParseRuleWindow(map[string]any{"cycles": 0, "duration": 0, "mode": "within"}); fw != nil || ww != nil {
		t.Fatalf("zero cycles+duration must be inert, got fw=%+v ww=%+v", fw, ww)
	}
	// A real within window still parses.
	if _, ww := ParseRuleWindow(map[string]any{"cycles": 3, "mode": "within"}); ww == nil || ww.Cycles != 3 {
		t.Fatalf("within cycles=3 must parse, got %+v", ww)
	}
}

func TestRecentSamplesZeroDurationKeepsAll(t *testing.T) {
	now := time.Unix(1000, 0)
	hist := []WindowSample{{At: now.Add(-time.Hour), Match: true}, {At: now, Match: false}}
	// duration <= 0 disables the time window, so every sample is kept.
	if got := recentSamples(hist, now, 0); len(got) != 2 {
		t.Fatalf("recentSamples(0 duration) = %d, want 2 (no time filtering)", len(got))
	}
}

func TestIsFiringAtDurationBoundary(t *testing.T) {
	at := time.Unix(1000, 0)
	r := Rule{For: &ForWindow{Duration: 6 * time.Minute}}
	s := &WindowState{}
	s.FiresAt(r, true, at) // prime trueSince
	// The read-only IsFiringAt fires at exactly the elapsed duration (>=).
	if !s.IsFiringAt(r, at.Add(6*time.Minute)) {
		t.Fatal("IsFiringAt must fire at exactly the duration")
	}
	if s.IsFiringAt(r, at.Add(5*time.Minute)) {
		t.Fatal("IsFiringAt must not fire before the duration")
	}
}
