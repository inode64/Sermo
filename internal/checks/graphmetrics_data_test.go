package checks

import (
	"context"
	"testing"
	"time"

	"sermo/internal/execx"
)

// TestGraphMetricsAreWrittenIntoResultData is the invariant that keeps a declared
// graph honest: a metric listed in graphMetrics has to be a numeric field the
// check actually publishes in Result.Data, because that is the only place the
// recorder reads it from. Declaring a key the check never writes costs nothing at
// build time and shows up in the dashboard as a panel that is permanently empty.
//
// Each case builds the real check through Build with an injected sampler, so the
// entry shapes here are the ones an operator writes.
func TestGraphMetricsAreWrittenIntoResultData(t *testing.T) {
	second := time.Second
	cases := []struct {
		typ   string
		entry map[string]any
		deps  Deps
	}{
		{CheckTypeStorage, map[string]any{"path": "/data", "used_pct": pred(">", "90%")}, Deps{DefaultTimeout: second, Samplers: Samplers{
			StorageUsage: func(string) (StorageStats, error) {
				return StorageStats{UsedPct: 42, FreePct: 58, UsedBytes: 42 << 30, FreeBytes: 58 << 30, TotalBytes: 100 << 30,
					InodesUsedPct: 7, InodesFreePct: 93, InodesFree: 930, InodesTotal: 1000}, nil
			},
			MountSampler: func() ([]Mount, error) {
				return []Mount{{MountPoint: "/data", Device: "/dev/sda1", FSType: "ext4"}}, nil
			},
		}}},
		{CheckTypeSwap, map[string]any{"metric": SwapMetricUsage, "used_pct": pred(">", "90%")}, Deps{DefaultTimeout: second, Samplers: Samplers{
			SwapSampler: func() (SwapSample, error) { return SwapSample{TotalBytes: 8 << 30, FreeBytes: 6 << 30}, nil },
		}}},
		{CheckTypeMemory, map[string]any{"used_pct": pred(">", "90%")}, Deps{DefaultTimeout: second, Samplers: Samplers{
			MemorySampler: func() (MemorySample, error) { return MemorySample{TotalBytes: 8 << 30, AvailableBytes: 2 << 30}, nil },
		}}},
		{CheckTypeLoad, map[string]any{"load1": pred(">", 4)}, Deps{DefaultTimeout: second, Samplers: Samplers{
			LoadSampler: func() (LoadSample, error) { return LoadSample{Load1: 1.5, Load5: 1.2, Load15: 0.9, NumCPU: 4}, nil },
		}}},
		{CheckTypePressure, map[string]any{"resource": "cpu", "some_avg10": pred(">", 10)}, Deps{DefaultTimeout: second, Samplers: Samplers{
			PressureSampler: func(string) (PressureSample, error) {
				return PressureSample{Some: PressureAverages{Avg10: 1.5, Avg60: 1.2, Avg300: 0.8}, Full: PressureAverages{Avg10: 0.3}}, nil
			},
		}}},
		{CheckTypeFDS, map[string]any{"used_pct": pred(">", "90%")}, Deps{DefaultTimeout: second, Samplers: Samplers{
			FdsSampler: func() (FdsSample, error) { return FdsSample{Allocated: 2048, Max: 65536}, nil },
		}}},
		{CheckTypePIDs, map[string]any{"used_pct": pred(">", "90%")}, Deps{DefaultTimeout: second, Samplers: Samplers{
			PidsSampler: func() (PidsSample, error) { return PidsSample{Threads: 900, Max: 32768}, nil },
		}}},
		{CheckTypeConntrack, map[string]any{"used_pct": pred(">", "90%")}, Deps{DefaultTimeout: second, Samplers: Samplers{
			ConntrackSampler: func() (ConntrackSample, error) { return ConntrackSample{Count: 1200, Max: 262144}, nil },
		}}},
		{CheckTypeEntropy, map[string]any{"avail": pred("<", 200)}, Deps{DefaultTimeout: second, Samplers: Samplers{
			EntropySampler: func() (uint64, bool) { return 3072, true },
		}}},
		{CheckTypeZombies, map[string]any{"count": pred(">", 5)}, Deps{DefaultTimeout: second, Samplers: Samplers{
			ZombieSampler: func() (uint64, bool) { return 2, true },
		}}},
		{CheckTypeFirewallRules, map[string]any{"backend": "nft", "min_rules": 2}, Deps{DefaultTimeout: second, Samplers: Samplers{
			FirewallRulesSampler: func(context.Context, string, execx.Runner) (FirewallRulesSample, error) {
				return FirewallRulesSample{Backend: FirewallBackendNftables, Rules: 17}, nil
			},
		}}},
		{CheckTypeInotify, map[string]any{"used_pct": pred(">", "90%")}, Deps{DefaultTimeout: second, Samplers: Samplers{
			InotifySampler: func(context.Context, bool) (InotifySample, error) {
				return InotifySample{MaxInstances: 1024, MaxWatches: 524288, WatchesRead: true,
					Users: []InotifyUserUsage{{UID: 0, Instances: 36, Watches: 400}}}, nil
			},
		}}},
		{CheckTypeSize, map[string]any{"path": "/var/log", "grow_by": "10M", "within": "1h"}, Deps{DefaultTimeout: second, Samplers: Samplers{
			SizeSampler: func(context.Context, string, bool) (int64, error) { return 4 << 20, nil },
		}}},
	}

	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			declared := GraphMetrics(tc.typ)
			if len(declared) == 0 {
				t.Fatalf("%s declares no graph metrics; drop it from this table", tc.typ)
			}
			res := buildOneCheck(t, "c", tc.typ, tc.entry, tc.deps).Run(context.Background())
			for _, m := range declared {
				raw, ok := res.Data[m.Key]
				if !ok {
					t.Errorf("%s declares graph metric %q but its result carries no such field: %v", tc.typ, m.Key, res.Data)
					continue
				}
				if _, numeric := NumericData(raw); !numeric {
					t.Errorf("%s graph metric %q is %T (%v), which the recorder cannot graph", tc.typ, m.Key, raw, raw)
				}
			}
		})
	}
}

// pred builds one {op, value} predicate entry, the shape every level check reads.
func pred(op string, value any) map[string]any {
	return map[string]any{"op": op, "value": value}
}

// TestResolvedGraphMetricsSeparatesUnitFromExistence pins the distinction the
// API depends on, now through the one resolver every consumer shares: a
// unitless metric is a real series, a declared `unit:` scalar appears as
// `value`, an icmp state watch resolves no latency series, and a banded key
// leaves the line set.
func TestResolvedGraphMetricsSeparatesUnitFromExistence(t *testing.T) {
	find := func(list []GraphMetric, key string) (GraphMetric, bool) {
		for _, m := range list {
			if m.Key == key {
				return m, true
			}
		}
		return GraphMetric{}, false
	}
	load := ResolvedGraphMetrics(CheckTypeLoad, "", map[string]any{})
	if m, ok := find(load, DataKeyLoad1); !ok || m.Unit != "" {
		t.Fatalf("load1 = %+v ok=%v, want a published unitless series", m, ok)
	}
	if _, ok := find(load, "not_a_field"); ok {
		t.Fatal("an unknown key must not resolve")
	}
	sql := ResolvedGraphMetrics(CheckTypeSQL, "MiB", map[string]any{})
	if m, ok := find(sql, DataKeyValue); !ok || m.Unit != "MiB" {
		t.Fatalf("declared scalar = %+v ok=%v, want the MiB value series", m, ok)
	}
	latency := ResolvedGraphMetrics(CheckTypeICMP, "", map[string]any{CheckKeyMetric: IcmpMetricLatency})
	if _, ok := find(latency, DataKeyLatencyMS); !ok {
		t.Fatal("an icmp latency watch must resolve its latency series")
	}
	state := ResolvedGraphMetrics(CheckTypeICMP, "", map[string]any{CheckKeyMetric: "state"})
	if _, ok := find(state, DataKeyLatencyMS); ok {
		t.Fatal("an icmp state watch publishes no latency; the panel must not be offered")
	}
	banded := ResolvedGraphMetrics(CheckTypeLoad, "", map[string]any{
		CheckKeyBands: map[string]any{
			DataKeyLoad1: map[string]any{
				CheckKeyOK:       map[string]any{CheckKeyOp: "<", CheckKeyValue: 8},
				CheckKeySeverity: SeverityWarning,
			},
		},
	})
	if _, ok := find(banded, DataKeyLoad1); ok {
		t.Fatal("a banded key must leave the line set")
	}
}

// TestUnreachableCountLimitIsNotALimit pins what a count-vs-limit check reports
// when the kernel gives it no ceiling to measure against: fs.file-max reads
// LONG_MAX on a host that lifts the cap, and 879116 of 9223372036854775807 is
// 0.0% — a gauge that could only ever say "plenty of headroom" and a used_pct
// threshold that could never hold. The count becomes the reading instead.
func TestUnreachableCountLimitIsNotALimit(t *testing.T) {
	const allocated = uint64(879116)
	for _, tc := range []struct {
		name    string
		limit   uint64
		message string
	}{
		{"no kernel limit", unlimitedCountMax, "fds 879116 allocated (no kernel limit)"},
		{"unreported limit", 0, "fds 879116 allocated (limit unknown)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := levelCountResult(base{name: "fds"}, []levelPred{{fieldUsedPct, ">=", 80}},
				"fds", "allocated", DataKeyAllocated, allocated, tc.limit, time.Now())
			if res.Message != tc.message {
				t.Errorf("message = %q, want %q", res.Message, tc.message)
			}
			for _, absent := range []string{fieldUsedPct, fieldFree, DataKeyMax} {
				if v, ok := res.Data[absent]; ok {
					t.Errorf("%s = %v, want absent: an unreachable ceiling is not a measurement", absent, v)
				}
			}
			if res.Data[DataKeyAllocated] != allocated {
				t.Errorf("allocated = %v, want %d", res.Data[DataKeyAllocated], allocated)
			}
			// The scalar is the count, not a percentage of nothing.
			if res.Data[DataKeyValue] != float64(allocated) {
				t.Errorf("value = %v, want the count %d", res.Data[DataKeyValue], allocated)
			}
			if res.OK {
				t.Error("a used_pct predicate must not hold when there is no percentage")
			}
		})
	}

	// A real ceiling still gauges, still reports a percentage, and the scalar is
	// that percentage.
	res := levelCountResult(base{name: "conntrack"}, []levelPred{{fieldUsedPct, ">=", 80}},
		"conntrack", "entries", DataKeyCount, 131072, 262144, time.Now())
	if res.Data[fieldUsedPct] != 50.0 || res.Data[DataKeyMax] != uint64(262144) || res.Data[DataKeyValue] != 50.0 {
		t.Fatalf("bounded sample = %+v", res.Data)
	}
}
