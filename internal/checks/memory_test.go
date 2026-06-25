package checks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func memSampler(total, available uint64) MemorySamplerFunc {
	return func() (MemorySample, error) { return MemorySample{TotalBytes: total, AvailableBytes: available}, nil }
}

func buildMemory(t *testing.T, entry map[string]any, sampler MemorySamplerFunc) memoryCheck {
	t.Helper()
	entry["type"] = "memory"
	built, warns := Build(map[string]any{"mem": entry}, Deps{DefaultTimeout: time.Second, MemorySampler: sampler})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("memory check should build: warns=%v", warns)
	}
	return built[0].Check.(memoryCheck)
}

func TestMemoryCheckLevels(t *testing.T) {
	// 8 GiB total, 1 GiB available -> 87.5% used.
	tight := memSampler(8<<30, 1<<30)

	fires := buildMemory(t, map[string]any{
		"used_pct": map[string]any{"op": ">=", "value": "85%"},
	}, tight)
	res := fires.Run(context.Background())
	if !res.OK || res.Data["used_pct"].(float64) != 87.5 {
		t.Fatalf("tight host must fire: OK=%v data=%v", res.OK, res.Data)
	}
	if res.Data["value"].(float64) != 87.5 || res.Data["available_bytes"].(uint64) != 1<<30 {
		t.Fatalf("result data = %v", res.Data)
	}
	// available_pct is the complement: 1 GiB of 8 GiB == 12.5%.
	if res.Data["available_pct"].(float64) != 12.5 {
		t.Fatalf("available_pct = %v, want 12.5", res.Data["available_pct"])
	}
	if !strings.Contains(res.Message, "87.5%") {
		t.Fatalf("message = %q", res.Message)
	}

	calm := buildMemory(t, map[string]any{
		"used_pct": map[string]any{"op": ">=", "value": "85%"},
	}, memSampler(8<<30, 6<<30))
	if calm.Run(context.Background()).OK {
		t.Fatal("a host at 25% used must not fire")
	}

	// available_bytes uses the shared size grammar (suffix required).
	lowBytes := buildMemory(t, map[string]any{
		"available_bytes": map[string]any{"op": "<", "value": "2G"},
	}, tight)
	if !lowBytes.Run(context.Background()).OK {
		t.Fatal("1GiB available < 2G must fire")
	}
}

func TestMemoryCheckGuards(t *testing.T) {
	zero := buildMemory(t, map[string]any{
		"used_pct": map[string]any{"op": ">=", "value": 1},
	}, memSampler(0, 0))
	if res := zero.Run(context.Background()); res.OK || !strings.Contains(res.Message, "unknown") {
		t.Fatalf("unknown total must never fire: %+v", res)
	}

	// MemAvailable above MemTotal (inconsistent snapshot) clamps instead of
	// underflowing into a huge used_pct.
	clamped := buildMemory(t, map[string]any{
		"used_pct": map[string]any{"op": ">", "value": 0},
	}, memSampler(1<<30, 2<<30))
	if res := clamped.Run(context.Background()); res.OK || res.Data["used_pct"].(float64) != 0 {
		t.Fatalf("clamped sample: %+v", res)
	}
}

func TestMemoryCheckRequiresPredicate(t *testing.T) {
	_, warns := Build(map[string]any{
		"mem": map[string]any{"type": "memory"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 1 || !strings.Contains(warns[0], "requires at least one of used_pct/available_pct/available_bytes") {
		t.Fatalf("warns = %v", warns)
	}
}
