package checks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func buildDiskIO(t *testing.T, entry map[string]any, samples []DiskIOSample) *diskIOCheck {
	t.Helper()
	entry["type"] = "diskio"
	if _, ok := entry["device"]; !ok {
		entry["device"] = "sda"
	}
	i := 0
	sampler := func(device string) (DiskIOSample, error) {
		if device != entry["device"] {
			t.Fatalf("sampler asked for %q", device)
		}
		s := samples[min(i, len(samples)-1)]
		i++
		return s, nil
	}
	built, warns := Build(map[string]any{"io": entry}, Deps{DefaultTimeout: time.Second, DiskIOSampler: sampler})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("diskio check should build: warns=%v", warns)
	}
	c := built[0].Check.(*diskIOCheck)
	// Deterministic 10s cycles.
	now := time.Unix(1_000_000, 0)
	c.clock = func() time.Time {
		now = now.Add(10 * time.Second)
		return now
	}
	return c
}

func TestDiskIOCheckRates(t *testing.T) {
	c := buildDiskIO(t, map[string]any{
		"util_pct":    map[string]any{"op": ">=", "value": "80%"},
		"write_bytes": map[string]any{"op": ">", "value": "1M"},
	}, []DiskIOSample{
		{},
		{
			// Over the 10s cycle: busy 9s of 10 (90%), 4096 sectors written
			// (2 MiB -> ~209715 B/s), 100 ops taking 1500ms total (15ms await).
			ReadsCompleted: 40, SectorsRead: 2048, ReadTicksMs: 500,
			WritesCompleted: 60, SectorsWritten: 4096, WriteTicksMs: 1000,
			IOTicksMs: 9000,
		},
	})

	if res := c.Run(context.Background()); res.OK || !strings.Contains(res.Message, "baseline") {
		t.Fatalf("first cycle must baseline: %+v", res)
	}

	res := c.Run(context.Background())
	if res.Data["util_pct"].(float64) != 90 {
		t.Fatalf("util_pct = %v, want 90", res.Data["util_pct"])
	}
	if got := res.Data["write_bytes"].(float64); got != 4096.0*512/10 {
		t.Fatalf("write_bytes = %v, want %v", got, 4096.0*512/10)
	}
	if got := res.Data["await_ms"].(float64); got != 15 {
		t.Fatalf("await_ms = %v, want 15", got)
	}
	// 90% util but only ~0.2 MB/s written: the write predicate fails, so the
	// level check (an AND) must not fire.
	if res.OK {
		t.Fatal("AND of predicates must not fire when one fails")
	}

	utilOnly := buildDiskIO(t, map[string]any{
		"util_pct": map[string]any{"op": ">=", "value": "80%"},
	}, []DiskIOSample{
		{},
		{WritesCompleted: 60, SectorsWritten: 4096, WriteTicksMs: 1000, IOTicksMs: 9000},
	})
	utilOnly.Run(context.Background())
	if !utilOnly.Run(context.Background()).OK {
		t.Fatal("90% util alone must fire a >=80% predicate")
	}
}

func TestDiskIOCounterResetClamps(t *testing.T) {
	c := buildDiskIO(t, map[string]any{
		"util_pct": map[string]any{"op": ">", "value": 0},
	}, []DiskIOSample{
		{IOTicksMs: 50_000, SectorsWritten: 1 << 30},
		{IOTicksMs: 100, SectorsWritten: 10}, // device reset: counters went backwards
	})
	c.Run(context.Background())
	res := c.Run(context.Background())
	if res.OK || res.Data["util_pct"].(float64) != 0 {
		t.Fatalf("reset counters must clamp to zero, got %v", res.Data)
	}
}

func TestDiskIOBuildErrors(t *testing.T) {
	for name, entry := range map[string]map[string]any{
		"no device":    {"type": "diskio", "util_pct": map[string]any{"op": ">", "value": 80}},
		"no predicate": {"type": "diskio", "device": "sda"},
	} {
		_, warns := Build(map[string]any{"io": entry}, Deps{DefaultTimeout: time.Second})
		if len(warns) != 1 {
			t.Errorf("%s: warns = %v, want one", name, warns)
		}
	}
}

func TestDefaultDiskIOSampler(t *testing.T) {
	if _, err := defaultDiskIOSampler("sermo-no-such-device"); err == nil {
		t.Fatal("unknown device must error")
	}
	// Parse a real device when the host has one (skip otherwise).
	data, err := defaultDiskIOSampler("sda")
	if err != nil {
		t.Skipf("no sda on this host: %v", err)
	}
	if data.ReadsCompleted == 0 && data.SectorsRead == 0 && data.IOTicksMs == 0 {
		t.Fatalf("implausible all-zero sample: %+v", data)
	}
}
