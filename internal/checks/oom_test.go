package checks

import (
	"context"
	"testing"
)

// scriptedOom returns a sampler that yields the given counts on successive calls.
func scriptedOom(counts ...uint64) OomSamplerFunc {
	i := 0
	return func() (uint64, bool) {
		c := counts[i]
		if i < len(counts)-1 {
			i++
		}
		return c, true
	}
}

func TestOomDeltaPrimesThenFires(t *testing.T) {
	c := &oomCheck{name: "o", op: ">", value: 0, sampler: scriptedOom(5, 5, 7)}

	if res := c.Run(context.Background()); res.OK {
		t.Fatal("first cycle must prime the baseline and not fire")
	}
	if res := c.Run(context.Background()); res.OK {
		t.Fatal("no new kills (5->5) must not fire")
	}
	res := c.Run(context.Background()) // 5 -> 7: two kills
	if !res.OK {
		t.Fatalf("two new kills should fire > 0, got %q", res.Message)
	}
	if res.Data["value"] != uint64(2) || res.Data["total"] != uint64(7) {
		t.Fatalf("unexpected data: %+v", res.Data)
	}
}

func TestOomThresholdAboveOne(t *testing.T) {
	// delta > 3: a single kill does not fire, a burst does.
	c := &oomCheck{name: "o", op: ">", value: 3, sampler: scriptedOom(0, 1, 10)}
	c.Run(context.Background()) // prime at 0
	if c.Run(context.Background()).OK {
		t.Fatal("delta 1 should not fire > 3")
	}
	if !c.Run(context.Background()).OK {
		t.Fatal("delta 9 should fire > 3")
	}
}

func TestOomCounterUnavailable(t *testing.T) {
	c := &oomCheck{name: "o", op: ">", value: 0, sampler: func() (uint64, bool) { return 0, false }}
	if c.Run(context.Background()).OK {
		t.Fatal("an unavailable oom_kill counter must never fire")
	}
}

func TestBuildOomCheckDefaultsToAnyKill(t *testing.T) {
	// `check: {type: oom}` with no delta must build and default to > 0.
	built, warns := Build(map[string]any{"o": map[string]any{"type": "oom"}}, Deps{OomSampler: scriptedOom(0, 1)})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 {
		t.Fatalf("expected 1 built check, got %d", len(built))
	}
	built[0].Check.Run(context.Background()) // prime
	if !built[0].Check.Run(context.Background()).OK {
		t.Fatal("default oom check should fire on any kill (delta 1 > 0)")
	}

	if _, warns := Build(map[string]any{"o": map[string]any{"type": "oom", "delta": map[string]any{"op": "=>", "value": 1}}}, Deps{}); len(warns) == 0 {
		t.Fatal("invalid oom delta op should warn")
	}
}

// TestOomConditionHoldsForOneCycleOnly pins why an oom watch must never sit behind a
// multi-cycle `for:` window. The kernel counter is cumulative and Run consumes the
// delta, so one kill makes the condition true for exactly one cycle: a window asking
// for two or more consecutive true cycles can never be satisfied, and the kill is
// reported nowhere. Contrast the rate-shaped deltas (swap io, net errors), which stay
// true while the pressure lasts and for which a window is meaningful.
func TestOomConditionHoldsForOneCycleOnly(t *testing.T) {
	// A single kill (7 -> 8), then the counter sits still as it does between events.
	c := &oomCheck{name: "o", op: ">", value: 0, sampler: scriptedOom(7, 8, 8, 8)}
	c.Run(context.Background()) // prime the baseline

	streak, longest := 0, 0
	for range 3 {
		if c.Run(context.Background()).OK {
			streak++
			longest = max(longest, streak)
			continue
		}
		streak = 0
	}
	if longest != 1 {
		t.Fatalf("longest run of consecutive met cycles = %d, want exactly 1", longest)
	}
}
