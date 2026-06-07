package checks

import (
	"context"
	"testing"
)

func fakeConntrack(s ConntrackSample) ConntrackSamplerFunc {
	return func() (ConntrackSample, error) { return s, nil }
}

func TestConntrackUsedPct(t *testing.T) {
	sample := ConntrackSample{Count: 92000, Max: 100000} // 92%
	breach := conntrackCheck{base: base{name: "c"}, preds: []conntrackPred{{"used_pct", ">=", 90}}, sampler: fakeConntrack(sample)}
	if res := breach.Run(context.Background()); !res.OK {
		t.Fatalf("92%% should breach >= 90, got %q", res.Message)
	}
	ok := conntrackCheck{base: base{name: "c"}, preds: []conntrackPred{{"used_pct", ">=", 95}}, sampler: fakeConntrack(sample)}
	if ok.Run(context.Background()).OK {
		t.Fatal("92%% should not breach >= 95")
	}
}

func TestConntrackFreeAbsolute(t *testing.T) {
	c := conntrackCheck{base: base{name: "c"}, preds: []conntrackPred{{"free", "<", 10000}}, sampler: fakeConntrack(ConntrackSample{Count: 95000, Max: 100000})}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("5000 free < 10000 should fire, got %q", res.Message)
	}
	if res.Data["free"] != uint64(5000) {
		t.Fatalf("data free = %v, want 5000", res.Data["free"])
	}
}

func TestConntrackUnknownMaxNeverFires(t *testing.T) {
	c := conntrackCheck{base: base{name: "c"}, preds: []conntrackPred{{"free", "<", 10000}}, sampler: fakeConntrack(ConntrackSample{Count: 95000, Max: 0})}
	if c.Run(context.Background()).OK {
		t.Fatal("a used_pct/free predicate must not fire when the limit is unknown")
	}
}

func TestBuildConntrackCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"c": map[string]any{"type": "conntrack", "used_pct": map[string]any{"op": ">=", "value": 80}},
	}, Deps{ConntrackSampler: fakeConntrack(ConntrackSample{Count: 85000, Max: 100000})})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 || !built[0].Check.Run(context.Background()).OK {
		t.Fatal("85%% should build and fire >= 80")
	}

	if _, warns := Build(map[string]any{"c": map[string]any{"type": "conntrack"}}, Deps{}); len(warns) == 0 {
		t.Fatal("conntrack check without a predicate should warn")
	}
}
