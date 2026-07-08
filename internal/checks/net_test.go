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
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: NetMetricState, expect: NetStateDown,
		sampler: sampler(NetSample{State: NetStateDown})}
	res := c.Run(context.Background())
	if !res.OK || res.Data[DataKeyValue] != NetStateDown || res.Data[DataKeyInterface] != "eth0" {
		t.Fatalf("expect-down should fire: %+v", res)
	}
	c2 := &netCheck{base: base{name: "n"}, iface: "eth0", metric: NetMetricState, expect: NetStateDown,
		sampler: sampler(NetSample{State: NetStateUp})}
	if c2.Run(context.Background()).OK {
		t.Fatal("expect-down must not fire when up")
	}
}

func TestNetStateOnChange(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: NetMetricState, onChange: true,
		sampler: sampler(NetSample{State: NetStateUp}, NetSample{State: NetStateDown})}
	if c.Run(context.Background()).OK {
		t.Fatal("first cycle must prime, not fire")
	}
	res := c.Run(context.Background())
	if !res.OK || res.Data[fieldOld] != NetStateUp || res.Data[fieldNew] != NetStateDown {
		t.Fatalf("state change should fire with old/new: %+v", res)
	}
	if c.Run(context.Background()).OK { // down -> down, no change
		t.Fatal("no change must not fire")
	}
}

func TestNetSpeedOnChange(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: NetMetricSpeed, onChange: true,
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
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: NetMetricSpeed, onChange: true,
		sampler: sampler(NetSample{SpeedKnown: false})}
	if c.Run(context.Background()).OK {
		t.Fatal("unknown speed must not fire")
	}
}

func TestNetErrorsDelta(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: NetMetricErrors,
		counters: []string{NetCounterRXErrors, NetCounterTXErrors}, op: ">", value: 100,
		sampler: sampler(
			NetSample{Counters: map[string]uint64{NetCounterRXErrors: 10, NetCounterTXErrors: 0}},
			NetSample{Counters: map[string]uint64{NetCounterRXErrors: 200, NetCounterTXErrors: 0}}, // +190
		)}
	if c.Run(context.Background()).OK {
		t.Fatal("first cycle primes (no delta)")
	}
	res := c.Run(context.Background())
	if !res.OK || res.Data[DataKeyValue] != uint64(190) {
		t.Fatalf("errors delta should fire with value 190: %+v", res)
	}
}

func TestNetErrorsCounterResetNoFire(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: NetMetricErrors,
		counters: []string{NetCounterRXErrors}, op: ">", value: 0,
		sampler: sampler(
			NetSample{Counters: map[string]uint64{NetCounterRXErrors: 500}},
			NetSample{Counters: map[string]uint64{NetCounterRXErrors: 0}}, // reset -> delta 0
		)}
	c.Run(context.Background())
	if c.Run(context.Background()).OK {
		t.Fatal("counter reset must yield delta 0 (no fire)")
	}
}

func TestNetSamplerError(t *testing.T) {
	c := &netCheck{base: base{name: "n"}, iface: "eth0", metric: NetMetricState, expect: NetStateUp,
		sampler: func(string) (NetSample, error) { return NetSample{}, errors.New("boom") }}
	if c.Run(context.Background()).OK {
		t.Fatal("sampler error must not fire")
	}
}

func TestSampleNetFromSysfsFallback(t *testing.T) {
	root := t.TempDir()
	iface := "sermo-test0"
	dir := filepath.Join(root, iface)
	statDir := filepath.Join(dir, sysfsIfaceStatisticsDir)
	if err := os.MkdirAll(statDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(dir, sysfsIfaceFlagsFile):     "0x1003\n",
		filepath.Join(dir, sysfsIfaceOperstateFile): NetStateUp + "\n",
		filepath.Join(dir, sysfsIfaceSpeedFile):     "1000\n",
		filepath.Join(statDir, NetCounterRXErrors):  "7\n",
		filepath.Join(statDir, NetCounterTXErrors):  "11\n",
		filepath.Join(statDir, "rx_dropped"):        "13\n",
		filepath.Join(statDir, "tx_dropped"):        "17\n",
		filepath.Join(statDir, "collisions"):        "19\n",
		filepath.Join(statDir, "multicast"):         "23\n",
		filepath.Join(statDir, "rx_packets"):        "29\n",
		filepath.Join(statDir, "tx_packets"):        "31\n",
		filepath.Join(statDir, "rx_bytes"):          "37\n",
		filepath.Join(statDir, "tx_bytes"):          "41\n",
		filepath.Join(statDir, "rx_overruns"):       "43\n",
		filepath.Join(statDir, "tx_overruns"):       "47\n",
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
	if sample.State != NetStateUp || !sample.SpeedKnown || sample.SpeedMbps != 1000 {
		t.Fatalf("sample = %+v, want up speed 1000", sample)
	}
	if sample.Counters[NetCounterRXErrors] != 7 || sample.Counters[NetCounterTXErrors] != 11 {
		t.Fatalf("counters = %+v, want rx/tx errors", sample.Counters)
	}
}

func TestSampleNetFromSysfsZeroSpeedKnown(t *testing.T) {
	root := t.TempDir()
	iface := "sermo-test1"
	dir := filepath.Join(root, iface)
	if err := os.MkdirAll(filepath.Join(dir, sysfsIfaceStatisticsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{sysfsIfaceFlagsFile: "0x1003\n", sysfsIfaceOperstateFile: NetStateUp + "\n", sysfsIfaceSpeedFile: "0\n"} {
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

func TestSampleNetFromSysfsMissingDirErrors(t *testing.T) {
	// A nonexistent interface whose sysfs dir is also missing must surface the
	// lookup error, not fabricate an empty "down" sample.
	if _, err := sampleNetFromSysfs("sermo-nope0", t.TempDir()); err == nil {
		t.Fatal("missing interface dir must return an error")
	}
}

func TestSysfsIfaceUp(t *testing.T) {
	mk := func(flags, operstate string) string {
		d := t.TempDir()
		if err := os.WriteFile(filepath.Join(d, sysfsIfaceFlagsFile), []byte(flags), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, sysfsIfaceOperstateFile), []byte(operstate), 0o644); err != nil {
			t.Fatal(err)
		}
		return d
	}
	// IFF_UP set: operstate "up" and "unknown" both count as up; "down" does not.
	if !sysfsIfaceUp(mk("0x1\n", NetStateUp+"\n")) {
		t.Error("operstate up must be up")
	}
	if !sysfsIfaceUp(mk("0x1\n", NetStateUnknown+"\n")) {
		t.Error("operstate unknown must be up")
	}
	if sysfsIfaceUp(mk("0x1\n", NetStateDown+"\n")) {
		t.Error("operstate down must be down")
	}
	// IFF_UP clear is never up.
	if sysfsIfaceUp(mk("0x0\n", NetStateUp+"\n")) {
		t.Error("IFF_UP clear must be down")
	}
}
