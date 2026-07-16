package checks

import (
	"context"
	"testing"
)

// Shared asserts for "count against a kernel limit" checks (conntrack, fds):
// used_pct windows, absolute free, free clamping, unknown limits, and Build.

// assertUsedPctWindow asserts the check built by mk breaches used_pct >= low
// but not used_pct >= high.
func assertUsedPctWindow(t *testing.T, mk func(preds []levelPred) Check, low, high float64) {
	t.Helper()
	if res := mk([]levelPred{{"used_pct", ">=", low}}).Run(context.Background()); !res.OK {
		t.Fatalf("should breach >= %v, got %q", low, res.Message)
	}
	if mk([]levelPred{{"used_pct", ">=", high}}).Run(context.Background()).OK {
		t.Fatalf("should not breach >= %v", high)
	}
}

// assertFreeFires asserts a free < limit predicate fires and exposes want.
func assertFreeFires(t *testing.T, c Check, want uint64) {
	t.Helper()
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("free should fire, got %q", res.Message)
	}
	if res.Data["free"] != want {
		t.Fatalf("data free = %v, want %d", res.Data["free"], want)
	}
}

// assertFreeClamps asserts free clamps to 0 (no unsigned underflow) when the
// sampled count exceeds the limit.
func assertFreeClamps(t *testing.T, c Check) {
	t.Helper()
	if got := c.Run(context.Background()).Data["free"]; got != uint64(0) {
		t.Fatalf("data free = %v, want 0 (clamped)", got)
	}
}

// assertUnknownMaxNeverFires asserts a used_pct/free predicate cannot hold
// when the limit is unknown (max == 0).
func assertUnknownMaxNeverFires(t *testing.T, c Check) {
	t.Helper()
	if c.Run(context.Background()).OK {
		t.Fatal("a used_pct/free predicate must not fire when the limit is unknown")
	}
}

// assertBuildLimitCheck builds a typ check at 85% usage with used_pct >= 80,
// asserts it fires, then asserts a predicate-less entry warns.
func assertBuildLimitCheck(t *testing.T, typ string, deps Deps) {
	t.Helper()
	built, warns := Build(map[string]any{
		"c": map[string]any{"type": typ, "used_pct": map[string]any{"op": ">=", "value": 80}},
	}, deps)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 || !built[0].Check.Run(context.Background()).OK {
		t.Fatal("85% usage should build and fire >= 80")
	}
	if _, warns := Build(map[string]any{"c": map[string]any{"type": typ}}, Deps{}); len(warns) == 0 {
		t.Fatalf("%s check without a predicate should warn", typ)
	}
}
