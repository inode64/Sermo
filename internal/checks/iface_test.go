package checks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestParseInterfaceMatch(t *testing.T) {
	for in, want := range map[string]bool{"": false, "any": false, "all": true} {
		if all, warn := parseInterfaceMatch(map[string]any{"interface_match": in}); warn != "" || all != want {
			t.Fatalf("%q -> %v/%q, want %v", in, all, warn, want)
		}
	}
	if _, warn := parseInterfaceMatch(map[string]any{"interface_match": "both"}); warn == "" {
		t.Fatal("invalid interface_match must warn")
	}
}

func TestTryInterfacesSharesTheDeadline(t *testing.T) {
	const budget = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	for _, matchAll := range []bool{false, true} {
		var shares []time.Duration
		_, _, _ = tryInterfaces(ctx, []string{"a", "b", "c"}, matchAll, func(attempt context.Context, _ string) error {
			deadline, ok := attempt.Deadline()
			if !ok {
				t.Fatal("an attempt lost the deadline")
			}
			shares = append(shares, time.Until(deadline))
			if !matchAll {
				return errors.New("failed fast")
			}
			return nil
		})
		// Finishing fast leaves the time to the next attempt, and the last one
		// gets all that is left.
		want := []time.Duration{budget / 3, budget / 2, budget}
		if len(shares) != len(want) {
			t.Fatalf("matchAll=%v: shares = %v, want %d attempts", matchAll, shares, len(want))
		}
		for i, share := range shares {
			if share < want[i]-time.Minute || share > want[i] {
				t.Fatalf("matchAll=%v: shares = %v, want about %v", matchAll, shares, want)
			}
		}
	}

	// No deadline, nothing to share.
	_, _, _ = tryInterfaces(context.Background(), []string{"a"}, false, func(attempt context.Context, _ string) error {
		if _, ok := attempt.Deadline(); ok {
			t.Fatal("an attempt invented a deadline")
		}
		return nil
	})
}

func TestTryInterfaces(t *testing.T) {
	// No interfaces: op runs once with "".
	ran := ""
	if c, per, err := tryInterfaces(context.Background(), nil, false, func(_ context.Context, i string) error { ran = "x" + i; return nil }); err != nil || c != "" || per != nil || ran != "x" {
		t.Fatalf("none: %q/%v/%v ran=%q", c, err, per, ran)
	}

	fail := func(bad string) func(context.Context, string) error {
		return func(_ context.Context, i string) error {
			if i == bad {
				return errors.New("fail " + i)
			}
			return nil
		}
	}

	// any: first success wins; per-interface recorded.
	if c, per, err := tryInterfaces(context.Background(), []string{"a", "b"}, false, fail("a")); err != nil || c != "b" || per["a"] != "fail a" || per["b"] != "ok" {
		t.Fatalf("any: %q/%v/%v", c, err, per)
	}
	// any: none reachable -> error.
	if c, per, err := tryInterfaces(context.Background(), []string{"a", "b"}, false, func(_ context.Context, i string) error { return errors.New("fail " + i) }); err == nil || c != "" || per["a"] != "fail a" || per["b"] != "fail b" {
		t.Fatalf("any all-failing: %q/%v/%v", c, err, per)
	}
	// all: every interface must succeed.
	if c, per, err := tryInterfaces(context.Background(), []string{"a", "b"}, true, func(context.Context, string) error { return nil }); err != nil || c != "b" || per["a"] != "ok" || per["b"] != "ok" {
		t.Fatalf("all-ok: %q/%v/%v", c, err, per)
	}
	// all: first failure loses.
	if c, _, err := tryInterfaces(context.Background(), []string{"a", "b"}, true, fail("a")); err == nil || c != "a" {
		t.Fatalf("all with a failing: %q/%v", c, err)
	}
}

func TestICMPSampleAggregation(t *testing.T) {
	sampler := func(_ context.Context, _, iface string, _ int, _ time.Duration) (PingSample, error) {
		switch iface {
		case "up":
			return PingSample{Reachable: true, RTTKnown: true, RTTms: 5}, nil
		case "down":
			return PingSample{}, nil // valid sample, just unreachable
		default:
			return PingSample{}, errors.New("bad iface " + iface)
		}
	}

	// any: reachable if at least one interface reaches.
	c := &icmpCheck{host: "h", ifaces: []string{"down", "up"}}
	if s, err := c.sample(t.Context(), sampler); err != nil || !s.Reachable {
		t.Fatalf("any: reachable=%v err=%v, want reachable", s.Reachable, err)
	}

	// all: not reachable unless every interface reaches.
	c.ifaceAll = true
	if s, _ := c.sample(t.Context(), sampler); s.Reachable {
		t.Fatal("all: must be unreachable when one interface is down")
	}
	c.ifaces = []string{"up", "up"}
	if s, err := c.sample(t.Context(), sampler); err != nil || !s.Reachable {
		t.Fatalf("all-up: reachable=%v err=%v, want reachable", s.Reachable, err)
	}
}

func TestIfaceSuffixAndData(t *testing.T) {
	// ifaceSuffix annotates only when an interface was selected.
	if got := ifaceSuffix(""); got != "" {
		t.Errorf("ifaceSuffix(\"\") = %q, want empty", got)
	}
	if got := ifaceSuffix("eth0"); got != " via eth0" {
		t.Errorf("ifaceSuffix(eth0) = %q, want \" via eth0\"", got)
	}
	// ifaceData wraps a non-nil per-interface map, and stays nil otherwise.
	if got := ifaceData(nil); got != nil {
		t.Errorf("ifaceData(nil) = %v, want nil", got)
	}
	got := ifaceData(map[string]any{"eth0": "ok"})
	if got == nil || got["interfaces"] == nil {
		t.Errorf("ifaceData(non-nil) = %v, want an interfaces map", got)
	}
}
