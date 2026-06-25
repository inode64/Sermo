package checks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestSampleNetFromSysfsFallback(t *testing.T) {
	root := t.TempDir()
	iface := "sermo-test0"
	dir := filepath.Join(root, iface)
	statDir := filepath.Join(dir, "statistics")
	if err := os.MkdirAll(statDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(dir, "flags"):           "0x1003\n",
		filepath.Join(dir, "operstate"):       "up\n",
		filepath.Join(dir, "speed"):           "1000\n",
		filepath.Join(statDir, "rx_errors"):   "7\n",
		filepath.Join(statDir, "tx_errors"):   "11\n",
		filepath.Join(statDir, "rx_dropped"):  "13\n",
		filepath.Join(statDir, "tx_dropped"):  "17\n",
		filepath.Join(statDir, "collisions"):  "19\n",
		filepath.Join(statDir, "multicast"):   "23\n",
		filepath.Join(statDir, "rx_packets"):  "29\n",
		filepath.Join(statDir, "tx_packets"):  "31\n",
		filepath.Join(statDir, "rx_bytes"):    "37\n",
		filepath.Join(statDir, "tx_bytes"):    "41\n",
		filepath.Join(statDir, "rx_overruns"): "43\n",
		filepath.Join(statDir, "tx_overruns"): "47\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	sample, err := sampleNetFromSysfs(iface, root)
	if err != nil {
		t.Fatal(err)
	}
	if sample.State != "up" || !sample.SpeedKnown || sample.SpeedMbps != 1000 {
		t.Fatalf("sample = %+v, want up speed 1000", sample)
	}
	if sample.Counters["rx_errors"] != 7 || sample.Counters["tx_errors"] != 11 {
		t.Fatalf("counters = %+v, want rx/tx errors", sample.Counters)
	}
}

func TestSampleNetFromSysfsZeroSpeedKnown(t *testing.T) {
	root := t.TempDir()
	iface := "sermo-test1"
	dir := filepath.Join(root, iface)
	if err := os.MkdirAll(filepath.Join(dir, "statistics"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"flags": "0x1003\n", "operstate": "up\n", "speed": "0\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sample, err := sampleNetFromSysfs(iface, root)
	if err != nil {
		t.Fatal(err)
	}
	// A reported speed of 0 is a known reading (v >= 0), not "unknown".
	if !sample.SpeedKnown || sample.SpeedMbps != 0 {
		t.Fatalf("speed 0 must be known, got %+v", sample)
	}
}
