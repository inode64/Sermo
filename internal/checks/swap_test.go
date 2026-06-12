package checks

import (
	"context"
	"testing"
)

func fakeSwap(s SwapSample) SwapSamplerFunc {
	return func() (SwapSample, error) { return s, nil }
}

func TestSwapUsageThreshold(t *testing.T) {
	// 800/1000 used = 80%.
	sample := SwapSample{TotalBytes: 1000, FreeBytes: 200}
	breached := &swapCheck{base: base{name: "s"}, metric: "usage",
		preds: []levelPred{{field: "used_pct", op: ">=", value: 80}}, sampler: fakeSwap(sample)}
	if res := breached.Run(context.Background()); !res.OK {
		t.Fatalf("80%% used should breach >= 80, got %q", res.Message)
	}
	ok := &swapCheck{base: base{name: "s"}, metric: "usage",
		preds: []levelPred{{field: "used_pct", op: ">=", value: 90}}, sampler: fakeSwap(sample)}
	if ok.Run(context.Background()).OK {
		t.Fatal("80%% used should not breach >= 90")
	}
}

func TestSwapUsageFreeBytes(t *testing.T) {
	c := &swapCheck{base: base{name: "s"}, metric: "usage",
		preds: []levelPred{{field: "free_bytes", op: "<", value: 500}}, sampler: fakeSwap(SwapSample{TotalBytes: 1000, FreeBytes: 200})}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("200 free < 500 should fire, got %q", res.Message)
	}
	if res.Data["value"] != 200.0 {
		t.Fatalf("data value = %v, want the free_bytes reading 200", res.Data["value"])
	}
}

func TestSwapUsageNoSwapNeverFires(t *testing.T) {
	c := &swapCheck{base: base{name: "s"}, metric: "usage",
		preds: []levelPred{{field: "free_bytes", op: "<", value: 500}}, sampler: fakeSwap(SwapSample{TotalBytes: 0, FreeBytes: 0})}
	if c.Run(context.Background()).OK {
		t.Fatal("a swapless host must never fire the usage check")
	}
}

func TestSwapIODeltaPrimes(t *testing.T) {
	samples := []SwapSample{
		{PagesIn: 100, PagesOut: 100}, // baseline total 200
		{PagesIn: 150, PagesOut: 200}, // total 350 -> delta 150
		{PagesIn: 150, PagesOut: 210}, // total 360 -> delta 10
	}
	i := 0
	c := &swapCheck{base: base{name: "s"}, metric: "io", op: ">", value: 100,
		sampler: func() (SwapSample, error) { s := samples[i]; i++; return s, nil }}

	if res := c.Run(context.Background()); res.OK {
		t.Fatal("first cycle must prime the baseline and not fire")
	}
	if res := c.Run(context.Background()); !res.OK {
		t.Fatalf("delta 150 > 100 should fire, got %q", res.Message)
	}
	if res := c.Run(context.Background()); res.OK {
		t.Fatal("delta 10 should not fire > 100")
	}
}

func TestBuildSwapChecks(t *testing.T) {
	usage, warn := buildCheckForTest(t, map[string]any{
		"type": "swap", "metric": "usage", "used_pct": map[string]any{"op": ">=", "value": 80},
	})
	if warn != "" {
		t.Fatalf("usage build warn: %s", warn)
	}
	if _, ok := usage.(*swapCheck); !ok {
		t.Fatalf("expected *swapCheck, got %T", usage)
	}

	if _, warn := buildCheckForTest(t, map[string]any{"type": "swap", "metric": "io"}); warn == "" {
		t.Fatal("swap io without delta should warn")
	}
	if _, warn := buildCheckForTest(t, map[string]any{"type": "swap", "metric": "bogus"}); warn == "" {
		t.Fatal("unknown swap metric should warn")
	}
}

// buildCheckForTest exercises the build path for a single inline check entry.
func buildCheckForTest(t *testing.T, entry map[string]any) (Check, string) {
	t.Helper()
	c, err := BuildInline("s", entry, Deps{})
	if err != nil {
		return nil, err.Error()
	}
	return c, ""
}
