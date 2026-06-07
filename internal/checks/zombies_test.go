package checks

import (
	"context"
	"testing"
)

func fakeZombies(n uint64, ok bool) ZombieSamplerFunc {
	return func() (uint64, bool) { return n, ok }
}

func TestZombieThreshold(t *testing.T) {
	over := zombieCheck{base: base{name: "z"}, op: ">", value: 20, sampler: fakeZombies(35, true)}
	if res := over.Run(context.Background()); !res.OK {
		t.Fatalf("35 should breach > 20, got %q", res.Message)
	}
	if over.Run(context.Background()).Data["zombies"] != uint64(35) {
		t.Fatal("data zombies should be 35")
	}
	under := zombieCheck{base: base{name: "z"}, op: ">", value: 20, sampler: fakeZombies(3, true)}
	if under.Run(context.Background()).OK {
		t.Fatal("3 should not breach > 20")
	}
}

func TestZombieUnavailableNeverFires(t *testing.T) {
	c := zombieCheck{base: base{name: "z"}, op: ">", value: 0, sampler: fakeZombies(0, false)}
	if c.Run(context.Background()).OK {
		t.Fatal("an unreadable /proc must never fire")
	}
}

func TestBuildZombieCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"z": map[string]any{"type": "zombies", "count": map[string]any{"op": ">", "value": 10}},
	}, Deps{ZombieSampler: fakeZombies(50, true)})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 || !built[0].Check.Run(context.Background()).OK {
		t.Fatal("50 > 10 should build and fire")
	}

	if _, warns := Build(map[string]any{"z": map[string]any{"type": "zombies"}}, Deps{}); len(warns) == 0 {
		t.Fatal("zombies check without count should warn")
	}
}
