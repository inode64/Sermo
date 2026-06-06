package checks

import (
	"context"
	"errors"
	"testing"
)

func sampler(samples ...NetSample) NetSamplerFunc {
	i := 0
	return func(string) (NetSample, error) {
		s := samples[i]
		if i < len(samples)-1 {
			i++
		}
		return s, nil
	}
}

func TestNetStateExpect(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "state", expect: "down",
		sampler: sampler(NetSample{State: "down"})}
	res := c.Run(context.Background())
	if !res.OK || res.Data["value"] != "down" || res.Data["interface"] != "eth0" {
		t.Fatalf("expect-down should fire: %+v", res)
	}
	c2 := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "state", expect: "down",
		sampler: sampler(NetSample{State: "up"})}
	if c2.Run(context.Background()).OK {
		t.Fatal("expect-down must not fire when up")
	}
}

func TestNetStateOnChange(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "state", onChange: true,
		sampler: sampler(NetSample{State: "up"}, NetSample{State: "down"})}
	if c.Run(context.Background()).OK {
		t.Fatal("first cycle must prime, not fire")
	}
	res := c.Run(context.Background())
	if !res.OK || res.Data["old"] != "up" || res.Data["new"] != "down" {
		t.Fatalf("state change should fire with old/new: %+v", res)
	}
	if c.Run(context.Background()).OK { // down -> down, no change
		t.Fatal("no change must not fire")
	}
}

func TestNetSpeedOnChange(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "speed", onChange: true,
		sampler: sampler(
			NetSample{SpeedMbps: 1000, SpeedKnown: true},
			NetSample{SpeedMbps: 100, SpeedKnown: true},
		)}
	if c.Run(context.Background()).OK {
		t.Fatal("first cycle primes")
	}
	if !c.Run(context.Background()).OK {
		t.Fatal("speed change should fire")
	}
}

func TestNetSpeedUnknownDoesNotFire(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "speed", onChange: true,
		sampler: sampler(NetSample{SpeedKnown: false})}
	if c.Run(context.Background()).OK {
		t.Fatal("unknown speed must not fire")
	}
}

func TestNetErrorsDelta(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "errors",
		counters: []string{"rx_errors", "tx_errors"}, op: ">", value: 100,
		sampler: sampler(
			NetSample{Counters: map[string]uint64{"rx_errors": 10, "tx_errors": 0}},
			NetSample{Counters: map[string]uint64{"rx_errors": 200, "tx_errors": 0}}, // +190
		)}
	if c.Run(context.Background()).OK {
		t.Fatal("first cycle primes (no delta)")
	}
	res := c.Run(context.Background())
	if !res.OK || res.Data["value"] != uint64(190) {
		t.Fatalf("errors delta should fire with value 190: %+v", res)
	}
}

func TestNetErrorsCounterResetNoFire(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "errors",
		counters: []string{"rx_errors"}, op: ">", value: 0,
		sampler: sampler(
			NetSample{Counters: map[string]uint64{"rx_errors": 500}},
			NetSample{Counters: map[string]uint64{"rx_errors": 0}}, // reset -> delta 0
		)}
	c.Run(context.Background())
	if c.Run(context.Background()).OK {
		t.Fatal("counter reset must yield delta 0 (no fire)")
	}
}

func TestNetSamplerError(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: "state", expect: "up",
		sampler: func(string) (NetSample, error) { return NetSample{}, errors.New("boom") }}
	if c.Run(context.Background()).OK {
		t.Fatal("sampler error must not fire")
	}
}
