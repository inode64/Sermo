package rules

import (
	"slices"
	"testing"
	"time"
)

func TestRecentSamplesPreservesInput(t *testing.T) {
	at := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	valid := WindowSample{At: at}
	cutoff := WindowSample{At: at.Add(-time.Minute)}
	old := WindowSample{At: at.Add(-2 * time.Minute)}
	future := WindowSample{At: at.Add(time.Second)}
	for _, tt := range []struct {
		name        string
		input, want []WindowSample
	}{
		{name: "nil"},
		{name: "valid boundaries", input: []WindowSample{cutoff, valid}, want: []WindowSample{cutoff, valid}},
		{name: "mixed unordered", input: []WindowSample{valid, old, cutoff, {}, future, valid}, want: []WindowSample{valid, cutoff, valid}},
		{name: "invalid prefix", input: []WindowSample{old, valid}, want: []WindowSample{valid}},
		{name: "invalid suffix", input: []WindowSample{valid, old}, want: []WindowSample{valid}},
		{name: "all invalid", input: []WindowSample{old, {}, future}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := slices.Clone(tt.input)
			got := recentSamples(tt.input, at, time.Minute)
			if !slices.Equal(got, tt.want) || (got == nil) != (tt.want == nil) {
				t.Fatalf("recent samples = %v, want %v", got, tt.want)
			}
			if !slices.Equal(tt.input, before) {
				t.Fatal("filter mutated source history")
			}
		})
	}
}

func TestRecentSamplesRetainsValidHistoryWithoutAllocation(t *testing.T) {
	at := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	history := make([]WindowSample, 1024)
	for i := range history {
		history[i].At = at
	}
	var got []WindowSample
	if allocs := testing.AllocsPerRun(100, func() {
		got = recentSamples(history, at, time.Minute)
	}); allocs != 0 {
		t.Fatalf("valid history allocated %g times", allocs)
	}
	if &got[0] != &history[0] {
		t.Fatal("valid history was copied")
	}
}

func TestWindowClonePreservesIndependentHistories(t *testing.T) {
	at := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	original := &WindowState{
		consecutive: -1, clearConsecutive: -2,
		history: []bool{true}, timedHistory: []WindowSample{{At: at}},
		trueSince: at, clearSince: at, firing: true,
	}
	clone := original.Clone()
	clone.history[0] = false
	clone.timedHistory[0].At = time.Time{}
	if !original.history[0] || !original.timedHistory[0].At.Equal(at) {
		t.Fatal("clone aliases source histories")
	}
	if clone.consecutive != 0 || clone.clearConsecutive != 0 || !clone.firing || !clone.trueSince.Equal(at) || !clone.clearSince.Equal(at) {
		t.Fatalf("clone lost normalization or episode state: %+v", clone)
	}
}
