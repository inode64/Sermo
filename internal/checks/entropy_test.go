package checks

import (
	"context"
	"testing"
)

func fakeEntropy(avail uint64, ok bool) EntropySamplerFunc {
	return func() (uint64, bool) { return avail, ok }
}

func TestEntropyThreshold(t *testing.T) {
	low := entropyCheck{base: base{name: "e"}, op: "<", value: 200, sampler: fakeEntropy(120, true)}
	if res := low.Run(context.Background()); !res.OK {
		t.Fatalf("120 < 200 should fire, got %q", res.Message)
	}
	if low.Run(context.Background()).Data["avail"] != uint64(120) {
		t.Fatal("data avail should be 120")
	}
	high := entropyCheck{base: base{name: "e"}, op: "<", value: 200, sampler: fakeEntropy(3000, true)}
	if high.Run(context.Background()).OK {
		t.Fatal("3000 should not fire < 200")
	}
}

func TestEntropyUnavailableNeverFires(t *testing.T) {
	c := entropyCheck{base: base{name: "e"}, op: "<", value: 200, sampler: fakeEntropy(0, false)}
	if c.Run(context.Background()).OK {
		t.Fatal("an unreadable entropy_avail must never fire")
	}
}

func TestBuildEntropyCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"e": map[string]any{"type": "entropy", "avail": map[string]any{"op": "<", "value": 200}},
	}, Deps{EntropySampler: fakeEntropy(100, true)})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 || !built[0].Check.Run(context.Background()).OK {
		t.Fatal("100 < 200 should build and fire")
	}

	if _, warns := Build(map[string]any{"e": map[string]any{"type": "entropy"}}, Deps{}); len(warns) == 0 {
		t.Fatal("entropy check without avail should warn")
	}
}
