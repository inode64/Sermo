package checks

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestSameIPv4(t *testing.T) {
	want := net.ParseIP("192.0.2.10")
	cases := []struct {
		name string
		peer net.Addr
		ok   bool
	}{
		{"matching target", &net.IPAddr{IP: net.ParseIP("192.0.2.10")}, true},
		{"different host", &net.IPAddr{IP: net.ParseIP("192.0.2.11")}, false},
		{"nil peer", nil, false},
		{"wrong addr type", &net.UDPAddr{IP: net.ParseIP("192.0.2.10")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameIPv4(tc.peer, want); got != tc.ok {
				t.Fatalf("sameIPv4 = %v, want %v", got, tc.ok)
			}
		})
	}
}

func pinger(samples ...PingSample) PingSamplerFunc {
	i := 0
	return func(string, string, int, time.Duration) (PingSample, error) {
		s := samples[i]
		if i < len(samples)-1 {
			i++
		}
		return s, nil
	}
}

func TestICMPStateExpect(t *testing.T) {
	c := &icmpCheck{base: base{name: "p"}, host: "h", metric: "state", expect: "up",
		sampler: pinger(PingSample{Reachable: true})}
	res := c.Run(context.Background())
	if !res.OK || res.Data["value"] != "up" || res.Data["host"] != "h" {
		t.Fatalf("expect-up should fire when reachable: %+v", res)
	}
	c2 := &icmpCheck{base: base{name: "p"}, host: "h", metric: "state", expect: "up",
		sampler: pinger(PingSample{Reachable: false})}
	if c2.Run(context.Background()).OK {
		t.Fatal("expect-up must not fire when unreachable")
	}
}

func TestICMPStateOnChange(t *testing.T) {
	c := &icmpCheck{base: base{name: "p"}, host: "h", metric: "state", onChange: true,
		sampler: pinger(PingSample{Reachable: true}, PingSample{Reachable: false})}
	if c.Run(context.Background()).OK {
		t.Fatal("first cycle primes")
	}
	res := c.Run(context.Background())
	if !res.OK || res.Data["old"] != "up" || res.Data["new"] != "down" {
		t.Fatalf("state change should fire with old/new: %+v", res)
	}
}

func TestICMPLatencyThreshold(t *testing.T) {
	c := &icmpCheck{base: base{name: "p"}, host: "h", metric: "latency", hasThreshold: true, op: ">", value: 100,
		sampler: pinger(PingSample{Reachable: true, RTTms: 150, RTTKnown: true})}
	if !c.Run(context.Background()).OK {
		t.Fatal("rtt 150 > 100 should fire")
	}
	c2 := &icmpCheck{base: base{name: "p"}, host: "h", metric: "latency", hasThreshold: true, op: ">", value: 100,
		sampler: pinger(PingSample{Reachable: true, RTTms: 50, RTTKnown: true})}
	if c2.Run(context.Background()).OK {
		t.Fatal("rtt 50 should not fire")
	}
}

func TestICMPLatencyThresholdUnreachable(t *testing.T) {
	c := &icmpCheck{base: base{name: "p"}, host: "h", metric: "latency", hasThreshold: true, op: ">", value: 0,
		sampler: pinger(PingSample{Reachable: false, RTTKnown: false})}
	if c.Run(context.Background()).OK {
		t.Fatal("unreachable must not fire latency")
	}
}

func TestICMPLatencyChange(t *testing.T) {
	c := &icmpCheck{base: base{name: "p"}, host: "h", metric: "latency", hasChange: true, delta: 50,
		sampler: pinger(
			PingSample{Reachable: true, RTTms: 20, RTTKnown: true},
			PingSample{Reachable: true, RTTms: 100, RTTKnown: true}, // |100-20|=80 > 50
		)}
	if c.Run(context.Background()).OK {
		t.Fatal("first reachable cycle primes")
	}
	if !c.Run(context.Background()).OK {
		t.Fatal("latency jump should fire")
	}
}

func TestICMPLatencyChangeUnreachableNoCorrupt(t *testing.T) {
	c := &icmpCheck{base: base{name: "p"}, host: "h", metric: "latency", hasChange: true, delta: 50,
		sampler: pinger(
			PingSample{Reachable: true, RTTms: 20, RTTKnown: true}, // prime baseline 20
			PingSample{Reachable: false, RTTKnown: false},          // no fire, no baseline update
			PingSample{Reachable: true, RTTms: 25, RTTKnown: true}, // |25-20|=5 < 50 -> no fire
		)}
	c.Run(context.Background()) // prime
	if c.Run(context.Background()).OK {
		t.Fatal("unreachable cycle must not fire")
	}
	if c.Run(context.Background()).OK {
		t.Fatal("baseline must be preserved (25 vs primed 20, not vs unreachable)")
	}
}

func TestICMPSamplerError(t *testing.T) {
	c := &icmpCheck{base: base{name: "p"}, host: "h", metric: "state", expect: "up",
		sampler: func(string, string, int, time.Duration) (PingSample, error) { return PingSample{}, errors.New("boom") }}
	if c.Run(context.Background()).OK {
		t.Fatal("sampler error must not fire")
	}
}

func TestICMPLatencyChangeBoundaries(t *testing.T) {
	// A 40ms decrease is |Δ|=40 < 50: measured as a difference (not a sum) and
	// strictly below the delta, so it must not fire.
	dec := &icmpCheck{base: base{name: "p"}, host: "h", metric: "latency", hasChange: true, delta: 50,
		sampler: pinger(
			PingSample{Reachable: true, RTTms: 100, RTTKnown: true},
			PingSample{Reachable: true, RTTms: 60, RTTKnown: true},
		)}
	dec.Run(context.Background()) // prime 100
	if dec.Run(context.Background()).OK {
		t.Fatal("a 40ms decrease must not fire a 50ms-delta change")
	}
	// A jump of exactly the delta does not fire (strict >).
	eq := &icmpCheck{base: base{name: "p"}, host: "h", metric: "latency", hasChange: true, delta: 50,
		sampler: pinger(
			PingSample{Reachable: true, RTTms: 100, RTTKnown: true},
			PingSample{Reachable: true, RTTms: 150, RTTKnown: true},
		)}
	eq.Run(context.Background()) // prime 100
	if eq.Run(context.Background()).OK {
		t.Fatal("a change of exactly the delta must not fire")
	}
}
