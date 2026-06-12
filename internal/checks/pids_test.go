package checks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func buildPids(t *testing.T, entry map[string]any, sampler PidsSamplerFunc) pidsCheck {
	t.Helper()
	entry["type"] = "pids"
	built, warns := Build(map[string]any{"pids": entry}, Deps{DefaultTimeout: time.Second, PidsSampler: sampler})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("pids check should build: warns=%v", warns)
	}
	return built[0].Check.(pidsCheck)
}

func pidsSampler(threads, maxPids uint64) PidsSamplerFunc {
	return func() (PidsSample, error) { return PidsSample{Threads: threads, Max: maxPids}, nil }
}

func TestPidsCheckLevels(t *testing.T) {
	// 3600 of 4000: 90% of the table in use.
	tight := pidsSampler(3600, 4000)

	fires := buildPids(t, map[string]any{
		"used_pct": map[string]any{"op": ">=", "value": "85%"},
	}, tight)
	res := fires.Run(context.Background())
	if !res.OK || res.Data["used_pct"].(float64) != 90 || res.Data["free"].(uint64) != 400 {
		t.Fatalf("tight table must fire: OK=%v data=%v", res.OK, res.Data)
	}
	if !strings.Contains(res.Message, "3600/4000") {
		t.Fatalf("message = %q", res.Message)
	}

	calm := buildPids(t, map[string]any{
		"used_pct": map[string]any{"op": ">=", "value": "85%"},
	}, pidsSampler(1500, 4194304))
	if calm.Run(context.Background()).OK {
		t.Fatal("a roomy PID table must not fire")
	}

	absolute := buildPids(t, map[string]any{
		"count": map[string]any{"op": ">", "value": 3000},
	}, tight)
	if !absolute.Run(context.Background()).OK {
		t.Fatal("count predicate must fire at 3600 > 3000")
	}
}

func TestPidsCheckUnknownLimit(t *testing.T) {
	// With pid_max unreadable, used_pct/free are unknown and can never hold.
	c := buildPids(t, map[string]any{
		"used_pct": map[string]any{"op": ">", "value": 0},
	}, pidsSampler(3600, 0))
	if c.Run(context.Background()).OK {
		t.Fatal("an unknown limit must never satisfy used_pct")
	}
}

func TestPidsCheckRequiresPredicate(t *testing.T) {
	_, warns := Build(map[string]any{
		"pids": map[string]any{"type": "pids"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 1 || !strings.Contains(warns[0], "requires at least one of used_pct/free/count") {
		t.Fatalf("warns = %v", warns)
	}
}

func TestDefaultPidsSampler(t *testing.T) {
	s, err := defaultPidsSampler()
	if err != nil {
		t.Skipf("no /proc/loadavg on this host: %v", err)
	}
	if s.Threads == 0 || s.Max == 0 || s.Threads > s.Max {
		t.Fatalf("implausible sample: %+v", s)
	}
}
