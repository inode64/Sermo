package checks

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// assertStateExpect runs a state-expect check that must fire on a matching sample
// (reporting wantValue under valueKey and wantLabel under labelKey) and one that
// must stay quiet on a non-matching sample.
func assertStateExpect(t *testing.T, match, nonMatch Check, valueKey, wantValue, labelKey, wantLabel string) {
	t.Helper()
	res := match.Run(context.Background())
	if !res.OK || res.Data[valueKey] != wantValue || res.Data[labelKey] != wantLabel {
		t.Fatalf("expect should fire on a matching sample: %+v", res)
	}
	if nonMatch.Run(context.Background()).OK {
		t.Fatal("expect must not fire on a non-matching sample")
	}
}

func TestNetStateExpect(t *testing.T) {
	mk := func(state string) Check {
		return &netCheck{name: "n", iface: "eth0", metric: NetMetricState, expect: NetStateDown,
			sampler: sampler(NetSample{State: state})}
	}
	assertStateExpect(t, mk(NetStateDown), mk(NetStateUp), DataKeyValue, NetStateDown, DataKeyInterface, "eth0")
}

func TestNetStateOnChange(t *testing.T) {
	c := &netCheck{name: "n", iface: "eth0", metric: NetMetricState,
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
	c := &netCheck{name: "n", iface: "eth0", metric: NetMetricSpeed,
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
	c := &netCheck{name: "n", iface: "eth0", metric: NetMetricSpeed,
		sampler: sampler(NetSample{SpeedKnown: false})}
	if c.Run(context.Background()).OK {
		t.Fatal("unknown speed must not fire")
	}
}

func TestNetErrorsDelta(t *testing.T) {
	c := &netCheck{name: "n", iface: "eth0", metric: NetMetricErrors,
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
	c := &netCheck{name: "n", iface: "eth0", metric: NetMetricErrors,
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
	c := &netCheck{name: "n", iface: "eth0", metric: NetMetricState, expect: NetStateUp,
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
		filepath.Join(dir, SysfsIfaceFlagsFile):     "0x1003\n",
		filepath.Join(dir, SysfsIfaceOperstateFile): NetStateUp + "\n",
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
	for name, body := range map[string]string{SysfsIfaceFlagsFile: "0x1003\n", SysfsIfaceOperstateFile: NetStateUp + "\n", sysfsIfaceSpeedFile: "0\n"} {
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
		if err := os.WriteFile(filepath.Join(d, SysfsIfaceFlagsFile), []byte(flags), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, SysfsIfaceOperstateFile), []byte(operstate), 0o644); err != nil {
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

// writeNetSysfs builds one /sys/class/net/<iface> tree from a map of relative
// paths to contents. A value starting with "->" becomes a symlink, which is how
// sysfs spells the device and driver bindings.
//
// Paths are created in sorted order, so a parent is always made before anything
// nested under it: a path is a prefix of its own children and therefore sorts
// ahead of them. Map order would not be — writing "device/driver" first makes
// "device" a real directory, and the "device" symlink then fails as already
// existing, on whichever runs the map iterated that way.
func writeNetSysfs(t *testing.T, root, iface string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, iface)
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		content := files[rel]
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if target, ok := strings.CutPrefix(content, "->"); ok {
			if err := os.MkdirAll(filepath.Join(root, target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, target), path); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNetIdentityFromSysfsPhysicalPort is a verbatim capture of eth0 on
// server-15.example.invalid: an Intel E810 port on the `ice` driver.
func TestNetIdentityFromSysfsPhysicalPort(t *testing.T) {
	root := t.TempDir()
	writeNetSysfs(t, root, "eth0", map[string]string{
		"address":       "34:5a:60:00:1c:92\n",
		"mtu":           "1500\n",
		"duplex":        "full\n",
		"device":        "->devices/0000:0a:00.0",
		"device/driver": "->bus/pci/drivers/ice",
		"uevent":        "INTERFACE=eth0\nIFINDEX=2\n",
	})
	got := netIdentityFromSysfs("eth0", root)
	want := NetIdentity{MAC: "34:5a:60:00:1c:92", Driver: "ice", Bus: "0000:0a:00.0", MTU: 1500, Duplex: "full"}
	if got != want {
		t.Fatalf("identity = %+v, want %+v", got, want)
	}
}

// TestNetIdentityFromSysfsVirtualBridge is docker0 on the same host: no device
// link at all, so no driver and no bus, and the kind comes from uevent. Its
// duplex reads `unknown`, which is a word the kernel uses for "not applicable"
// and must not be published as a fact.
func TestNetIdentityFromSysfsVirtualBridge(t *testing.T) {
	root := t.TempDir()
	writeNetSysfs(t, root, "docker0", map[string]string{
		"address": "8a:fe:a7:f7:5a:a0\n",
		"mtu":     "1500\n",
		"duplex":  "unknown\n",
		"uevent":  "DEVTYPE=bridge\nINTERFACE=docker0\n",
	})
	got := netIdentityFromSysfs("docker0", root)
	want := NetIdentity{MAC: "8a:fe:a7:f7:5a:a0", Kind: "bridge", MTU: 1500}
	if got != want {
		t.Fatalf("identity = %+v, want %+v", got, want)
	}
}

// TestNetIdentityFromSysfsAbsentInterface keeps a vanished interface from
// inventing an identity out of missing files.
func TestNetIdentityFromSysfsAbsentInterface(t *testing.T) {
	if got := netIdentityFromSysfs("eth9", t.TempDir()); !got.empty() {
		t.Fatalf("identity = %+v, want empty for an interface with no sysfs directory", got)
	}
}

// TestNetCheckPublishesIdentityAndFlaps pins that a live sample carries what the
// interface is beside what it measured, including the kernel's link-transition
// count — a link that is up now but has flapped is not the same as one that has
// been up since boot, and a per-cycle sample cannot see between its own samples.
func TestNetCheckPublishesIdentityAndFlaps(t *testing.T) {
	identity := NetIdentity{MAC: "34:5a:60:00:1c:92", Driver: "ice", Bus: "0000:0a:00.0", MTU: 1500, Duplex: "full"}
	c := &netCheck{
		name: "net-eth0", iface: "eth0", metric: NetMetricState, expect: NetStateUp,
		sampler: func(string) (NetSample, error) {
			return NetSample{State: NetStateUp, Identity: identity, CarrierChanges: 7, CarrierChangesKnown: true}, nil
		},
	}
	res := c.Run(t.Context())
	if !res.OK {
		t.Fatalf("state check failed: %s", res.Message)
	}
	for key, want := range map[string]any{
		DataKeyMAC: "34:5a:60:00:1c:92", DataKeyDriver: "ice", DataKeyBus: "0000:0a:00.0",
		DataKeyDuplex: "full", DataKeyMTU: uint64(1500), DataKeyCarrierChanges: uint64(7),
	} {
		if got := res.Data[key]; got != want {
			t.Errorf("data[%s] = %v (%T), want %v", key, got, got, want)
		}
	}
	if _, present := res.Data[DataKeyKind]; present {
		t.Errorf("a physical port published a kind: %v", res.Data[DataKeyKind])
	}
}

// TestNetCheckNamesAVanishedInterface is the whole point of retaining identity.
// Unlike a disk, which keeps its /dev node and its sysfs identity after it goes
// quiet, a removed interface takes its sysfs directory with it — so without
// memory a failed sample can only say the name the operator already typed.
func TestNetCheckNamesAVanishedInterface(t *testing.T) {
	identity := NetIdentity{MAC: "34:5a:60:00:1c:93", Driver: "ice", Bus: "0000:0a:00.1", MTU: 1500}
	present := true
	c := &netCheck{
		name: "net-eth1", iface: "eth1", metric: NetMetricState, expect: NetStateUp,
		sampler: func(string) (NetSample, error) {
			if !present {
				return NetSample{}, errors.New("no such network interface")
			}
			return NetSample{State: NetStateUp, Identity: identity}, nil
		},
	}
	if res := c.Run(t.Context()); !res.OK {
		t.Fatalf("first sample failed: %s", res.Message)
	}

	present = false
	res := c.Run(t.Context())
	if !res.Unavailable {
		t.Fatalf("a missing interface must be unavailable, got %+v", res)
	}
	if got := res.Data[DataKeyMAC]; got != "34:5a:60:00:1c:93" {
		t.Errorf("data[mac] = %v, want the identity the interface had while it existed", got)
	}
	if got := res.Data[DataKeyBus]; got != "0000:0a:00.1" {
		t.Errorf("data[bus] = %v, want the retained bus address", got)
	}
}

// TestNetCheckInventsNoIdentityBeforeItObservedOne keeps the memory honest: a
// check that never saw the interface reports nothing about it, which is what a
// daemon restart leaves behind.
func TestNetCheckInventsNoIdentityBeforeItObservedOne(t *testing.T) {
	c := &netCheck{
		name: "net-eth9", iface: "eth9", metric: NetMetricState, expect: NetStateUp,
		sampler:  func(string) (NetSample, error) { return NetSample{}, errors.New("no such network interface") },
		identity: func(string) NetIdentity { return NetIdentity{} },
	}
	res := c.Run(t.Context())
	for _, key := range []string{DataKeyMAC, DataKeyDriver, DataKeyBus, DataKeyMTU} {
		if v, present := res.Data[key]; present {
			t.Errorf("data[%s] = %v, want nothing before the interface was ever observed", key, v)
		}
	}
}
