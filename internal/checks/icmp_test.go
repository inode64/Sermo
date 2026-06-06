package checks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func pinger(samples ...PingSample) PingSamplerFunc {
	i := 0
	return func(string, int, time.Duration) (PingSample, error) {
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
			PingSample{Reachable: true, RTTms: 20, RTTKnown: true},  // prime baseline 20
			PingSample{Reachable: false, RTTKnown: false},           // no fire, no baseline update
			PingSample{Reachable: true, RTTms: 25, RTTKnown: true},  // |25-20|=5 < 50 -> no fire
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
		sampler: func(string, int, time.Duration) (PingSample, error) { return PingSample{}, errors.New("boom") }}
	if c.Run(context.Background()).OK {
		t.Fatal("sampler error must not fire")
	}
}
