package rules

import (
	"testing"
	"time"
)

// withinWindow is the single source of within-window defaults; pin the
// configured/unconfigured boundary and the MinMatches default.
func TestWithinWindow(t *testing.T) {
	cases := []struct {
		name       string
		within     *WithinWindow
		cycles     int
		duration   time.Duration
		minMatches int
		ok         bool
	}{
		{"nil", nil, 0, 0, 0, false},
		{"empty: neither cycles nor duration", &WithinWindow{}, 0, 0, 0, false},
		{"cycles only defaults minMatches to 1", &WithinWindow{Cycles: 3}, 3, 0, 1, true},
		{"duration with explicit minMatches", &WithinWindow{Duration: 5 * time.Second, MinMatches: 2}, 0, 5 * time.Second, 2, true},
		{"non-positive minMatches defaults to 1", &WithinWindow{Cycles: 2, MinMatches: -1}, 2, 0, 1, true},
	}
	for _, c := range cases {
		cy, d, mm, ok := (Rule{Within: c.within}).withinWindow()
		if cy != c.cycles || d != c.duration || mm != c.minMatches || ok != c.ok {
			t.Errorf("%s: withinWindow() = (%d,%v,%d,%v), want (%d,%v,%d,%v)",
				c.name, cy, d, mm, ok, c.cycles, c.duration, c.minMatches, c.ok)
		}
	}
}

// forWindow defaults to a single cycle when no for-window is configured, and
// prefers a duration over a cycle count.
func TestForWindow(t *testing.T) {
	cases := []struct {
		name     string
		forw     *ForWindow
		cycles   int
		duration time.Duration
	}{
		{"nil defaults to 1 cycle", nil, 1, 0},
		{"empty defaults to 1 cycle", &ForWindow{}, 1, 0},
		{"duration", &ForWindow{Duration: 5 * time.Second}, 0, 5 * time.Second},
		{"cycles", &ForWindow{Cycles: 4}, 4, 0},
		{"duration takes precedence over cycles", &ForWindow{Cycles: 4, Duration: 2 * time.Second}, 0, 2 * time.Second},
	}
	for _, c := range cases {
		cy, d := (Rule{For: c.forw}).forWindow()
		if cy != c.cycles || d != c.duration {
			t.Errorf("%s: forWindow() = (%d,%v), want (%d,%v)", c.name, cy, d, c.cycles, c.duration)
		}
	}
}
