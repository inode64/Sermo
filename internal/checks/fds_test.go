package checks

import (
	"context"
	"testing"
)

func fakeFds(s FdsSample) FdsSamplerFunc {
	return func() (FdsSample, error) { return s, nil }
}

func TestFdsUsedPct(t *testing.T) {
	// 9000/10000 = 90%.
	sample := FdsSample{Allocated: 9000, Max: 10000}
	breach := fdsCheck{base: base{name: "f"}, preds: []fdsPred{{"used_pct", ">=", 90}}, sampler: fakeFds(sample)}
	if res := breach.Run(context.Background()); !res.OK {
		t.Fatalf("90%% used should breach >= 90, got %q", res.Message)
	}
	ok := fdsCheck{base: base{name: "f"}, preds: []fdsPred{{"used_pct", ">=", 95}}, sampler: fakeFds(sample)}
	if ok.Run(context.Background()).OK {
		t.Fatal("90%% used should not breach >= 95")
	}
}

func TestFdsFreeAbsolute(t *testing.T) {
	c := fdsCheck{base: base{name: "f"}, preds: []fdsPred{{"free", "<", 2000}}, sampler: fakeFds(FdsSample{Allocated: 9000, Max: 10000})}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("1000 free < 2000 should fire, got %q", res.Message)
	}
	if res.Data["free"] != uint64(1000) {
		t.Fatalf("data free = %v, want 1000", res.Data["free"])
	}
}

func TestFdsUnknownMaxNeverFires(t *testing.T) {
	// Max == 0 leaves used_pct/free unknown, so the predicate cannot hold.
	c := fdsCheck{base: base{name: "f"}, preds: []fdsPred{{"free", "<", 2000}}, sampler: fakeFds(FdsSample{Allocated: 9000, Max: 0})}
	if c.Run(context.Background()).OK {
		t.Fatal("a used_pct/free predicate must not fire when the limit is unknown")
	}
}

func TestBuildFdsCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"f": map[string]any{"type": "fds", "used_pct": map[string]any{"op": ">=", "value": 80}},
	}, Deps{FdsSampler: fakeFds(FdsSample{Allocated: 8500, Max: 10000})})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 || !built[0].Check.Run(context.Background()).OK {
		t.Fatal("85%% used should build and fire >= 80")
	}

	if _, warns := Build(map[string]any{"f": map[string]any{"type": "fds"}}, Deps{}); len(warns) == 0 {
		t.Fatal("fds check without a predicate should warn")
	}
}
