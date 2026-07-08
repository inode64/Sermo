package checks

import (
	"context"
	"testing"
)

func fakeLoad(s LoadSample) LoadSamplerFunc {
	return func() (LoadSample, error) { return s, nil }
}

func TestLoadThreshold(t *testing.T) {
	sample := LoadSample{Load1: 9, Load5: 7, Load15: 4, NumCPU: 8}
	breach := loadCheck{base: base{name: "l"}, preds: []levelPred{{"load5", ">", 5}}, sampler: fakeLoad(sample)}
	if res := breach.Run(context.Background()); !res.OK {
		t.Fatalf("load5 7 should breach > 5, got %q", res.Message)
	}
	ok := loadCheck{base: base{name: "l"}, preds: []levelPred{{"load5", ">", 10}}, sampler: fakeLoad(sample)}
	if ok.Run(context.Background()).OK {
		t.Fatal("load5 7 should not breach > 10")
	}
}

func TestLoadPerCPUNormalizes(t *testing.T) {
	// load5 = 7 on 8 cores -> 0.875 per core.
	sample := LoadSample{Load1: 9, Load5: 7, Load15: 4, NumCPU: 8}
	// Raw load5 (7) breaches > 1, but per-core (0.875) does not.
	perCPU := loadCheck{base: base{name: "l"}, perCPU: true, preds: []levelPred{{"load5", ">", 1.0}}, sampler: fakeLoad(sample)}
	if perCPU.Run(context.Background()).OK {
		t.Fatal("0.875 per core should not breach > 1.0")
	}
	// load1 = 9 on 8 cores -> 1.125 per core, breaches > 1.0.
	over := loadCheck{base: base{name: "l"}, perCPU: true, preds: []levelPred{{"load1", ">", 1.0}}, sampler: fakeLoad(sample)}
	res := over.Run(context.Background())
	if !res.OK {
		t.Fatalf("1.125 per core should breach > 1.0, got %q", res.Message)
	}
	if v := res.Data["value"].(float64); v < 1.12 || v > 1.13 {
		t.Fatalf("value should be the per-cpu load1 (~1.125), got %v", v)
	}
}

func TestLoadPerCPUNoCPUFails(t *testing.T) {
	c := loadCheck{base: base{name: "l"}, perCPU: true, preds: []levelPred{{"load1", ">", 1}}, sampler: fakeLoad(LoadSample{Load1: 9, NumCPU: 0})}
	if c.Run(context.Background()).OK {
		t.Fatal("per_cpu with unknown cpu count must not fire")
	}
}

func TestLoadMultiPredAnd(t *testing.T) {
	sample := LoadSample{Load1: 9, Load5: 7, Load15: 1, NumCPU: 1}
	// load5 > 5 (true) AND load15 > 5 (false) -> not OK.
	c := loadCheck{base: base{name: "l"}, preds: []levelPred{{"load5", ">", 5}, {"load15", ">", 5}}, sampler: fakeLoad(sample)}
	if c.Run(context.Background()).OK {
		t.Fatal("AND of predicates should not fire when one fails")
	}
}

func TestBuildLoadCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"l": map[string]any{
			CheckKeyType:   CheckTypeLoad,
			CheckKeyPerCPU: true,
			fieldLoad5:     map[string]any{CheckKeyOp: ">", CheckKeyValue: 1.0},
		},
	}, Deps{LoadSampler: fakeLoad(LoadSample{Load5: 8, NumCPU: 4})})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 || !built[0].Check.Run(context.Background()).OK {
		t.Fatal("load5 8/4=2.0 per core should build and fire > 1.0")
	}

	if _, warns := Build(map[string]any{"l": map[string]any{"type": "load"}}, Deps{}); len(warns) == 0 {
		t.Fatal("load check without a predicate should warn")
	}
}
