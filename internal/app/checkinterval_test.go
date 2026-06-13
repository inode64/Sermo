package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
)

func TestCheckIntervals(t *testing.T) {
	tree := map[string]any{"checks": map[string]any{
		"fast":    map[string]any{"type": "tcp"},                        // no interval -> every cycle
		"slow":    map[string]any{"type": "command", "interval": "30m"}, // 60 cycles
		"sub":     map[string]any{"type": "http", "interval": "10s"},    // below resolution
		"nonmult": map[string]any{"type": "http", "interval": "45s"},    // not a multiple
	}}
	every, warns := checkIntervals(tree, 30*time.Second)

	if _, ok := every["fast"]; ok {
		t.Fatalf("a check with no interval should not be in the map: %v", every)
	}
	if every["slow"] != 60 {
		t.Fatalf("slow every = %d, want 60", every["slow"])
	}
	if every["sub"] != 1 {
		t.Fatalf("sub-resolution every = %d, want 1 (clamped)", every["sub"])
	}
	if every["nonmult"] != 2 { // round(45/30)=2 -> 60s
		t.Fatalf("nonmult every = %d, want 2", every["nonmult"])
	}
	// two warnings: below-resolution and not-a-multiple.
	if len(warns) != 2 {
		t.Fatalf("warnings = %v, want 2 (sub + nonmult)", warns)
	}
}

func TestCheckIntervalsNonPositiveResolution(t *testing.T) {
	// A non-positive resolution would make round(d/resolution) divide by zero
	// (+Inf -> undefined int conversion); the guard returns no intervals instead.
	tree := map[string]any{"checks": map[string]any{
		"slow": map[string]any{"type": "command", "interval": "30m"},
	}}
	for _, res := range []time.Duration{0, -time.Second} {
		every, warns := checkIntervals(tree, res)
		if every != nil || warns != nil {
			t.Fatalf("resolution %s: got every=%v warns=%v, want nil,nil", res, every, warns)
		}
	}
}

func TestDueChecks(t *testing.T) {
	built := []checks.Built{
		{Check: stubCheck{name: "fast"}},
		{Check: stubCheck{name: "slow"}},
	}
	every := map[string]int{"slow": 3} // fast defaults to every cycle

	dueNames := func(cycle int) []string {
		var out []string
		for _, b := range dueChecks(cycle, built, every) {
			out = append(out, b.Check.Name())
		}
		return out
	}

	// fast runs every cycle; slow runs on cycles 1, 4, 7, …
	if got := dueNames(1); len(got) != 2 {
		t.Fatalf("cycle 1 should run all checks, got %v", got)
	}
	if got := dueNames(2); len(got) != 1 || got[0] != "fast" {
		t.Fatalf("cycle 2 should run only fast, got %v", got)
	}
	if got := dueNames(3); len(got) != 1 || got[0] != "fast" {
		t.Fatalf("cycle 3 should run only fast, got %v", got)
	}
	if got := dueNames(4); len(got) != 2 {
		t.Fatalf("cycle 4 should run fast and slow again, got %v", got)
	}
}

func TestPausedCyclesAdvanceCheckInterval(t *testing.T) {
	built := []checks.Built{
		{Check: stubCheck{name: "fast"}},
		{Check: stubCheck{name: "slow"}},
	}
	every := map[string]int{"slow": 3}
	cache := map[string]checks.Result{}

	paused := true
	var slowRan bool
	w := &Worker{IsPaused: func() bool { return paused }}
	w.Checks = func(_ context.Context, _ checks.Deps) map[string]checks.Result {
		for _, b := range dueChecks(w.cycle, built, every) {
			if b.Check.Name() == "slow" {
				slowRan = true
			}
		}
		return cache
	}

	for i := 0; i < 2; i++ {
		w.RunCycle(context.Background())
	}
	if w.cycle != 2 {
		t.Fatalf("after two paused ticks cycle = %d, want 2", w.cycle)
	}

	paused = false
	slowRan = false
	w.RunCycle(context.Background()) // cycle 3: only fast
	if slowRan {
		t.Fatal("slow check must not run on cycle 3 after two paused ticks")
	}

	slowRan = false
	w.RunCycle(context.Background()) // cycle 4: fast and slow
	if !slowRan {
		t.Fatal("slow check must run on cycle 4 to stay aligned with the scheduler")
	}
}
