package checks

import (
	"errors"
	"testing"
	"time"
)

func TestParseInterfaces(t *testing.T) {
	if got := parseInterfaces("eth0"); len(got) != 1 || got[0] != "eth0" {
		t.Fatalf("string = %v", got)
	}
	if got := parseInterfaces([]any{"eth0", "192.168.1.2"}); len(got) != 2 || got[1] != "192.168.1.2" {
		t.Fatalf("list = %v", got)
	}
	if got := parseInterfaces([]any{"eth0", "", 7, true}); len(got) != 1 || got[0] != "eth0" {
		t.Fatalf("mixed list = %v, want [eth0]", got)
	}
	if got := parseInterfaces(""); got != nil {
		t.Fatalf("empty = %v, want nil", got)
	}
	if got := parseInterfaces(nil); got != nil {
		t.Fatalf("nil = %v, want nil", got)
	}
}

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

func TestTryInterfaces(t *testing.T) {
	// No interfaces: op runs once with "".
	ran := ""
	if c, per, err := tryInterfaces(nil, false, func(i string) error { ran = "x" + i; return nil }); err != nil || c != "" || per != nil || ran != "x" {
		t.Fatalf("none: %q/%v/%v ran=%q", c, err, per, ran)
	}

	fail := func(bad string) func(string) error {
		return func(i string) error {
			if i == bad {
				return errors.New("fail " + i)
			}
			return nil
		}
	}

	// any: first success wins; per-interface recorded.
	if c, per, err := tryInterfaces([]string{"a", "b"}, false, fail("a")); err != nil || c != "b" || per["a"] != "fail a" || per["b"] != "ok" {
		t.Fatalf("any: %q/%v/%v", c, err, per)
	}
	// any: none reachable -> error.
	if _, _, err := tryInterfaces([]string{"a", "b"}, false, func(string) error { return errors.New("x") }); err == nil {
		t.Fatal("any with all failing must error")
	}
	// all: every interface must succeed.
	if _, _, err := tryInterfaces([]string{"a", "b"}, true, func(string) error { return nil }); err != nil {
		t.Fatalf("all-ok: %v", err)
	}
	// all: first failure loses.
	if c, _, err := tryInterfaces([]string{"a", "b"}, true, fail("a")); err == nil || c != "a" {
		t.Fatalf("all with a failing: %q/%v", c, err)
	}
}

func TestICMPSampleAggregation(t *testing.T) {
	sampler := func(_, iface string, _ int, _ time.Duration) (PingSample, error) {
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
	if s, err := c.sample(sampler); err != nil || !s.Reachable {
		t.Fatalf("any: reachable=%v err=%v, want reachable", s.Reachable, err)
	}

	// all: not reachable unless every interface reaches.
	c.ifaceAll = true
	if s, _ := c.sample(sampler); s.Reachable {
		t.Fatal("all: must be unreachable when one interface is down")
	}
	c.ifaces = []string{"up", "up"}
	if s, err := c.sample(sampler); err != nil || !s.Reachable {
		t.Fatalf("all-up: reachable=%v err=%v, want reachable", s.Reachable, err)
	}
}
