package app

import (
	"runtime"
	"testing"

	"sermo/internal/metrics"
)

func TestHostMetricLoad1Saturation(t *testing.T) {
	// load1 == CPU count is full saturation: the raw load stays in Absolute, and
	// the tile gets a 0-100% reading plus the CPU-count capacity for its bar.
	ncpu := runtime.NumCPU()
	m := hostMetric("load1", metrics.Reading{Absolute: float64(ncpu), HasAbsolute: true, Ready: true})
	if m.Absolute != float64(ncpu) {
		t.Fatalf("raw load must stay in Absolute, got %v", m.Absolute)
	}
	if m.Total != float64(ncpu) {
		t.Fatalf("Total = %v, want the CPU count %d", m.Total, ncpu)
	}
	if m.Percent < 99.9 || m.Percent > 100.1 {
		t.Fatalf("Percent = %v, want ~100 (fully saturated)", m.Percent)
	}
}

func TestCountMeter(t *testing.T) {
	m := countMeter("fds", 8000, 10000)
	if m == nil || m.Kind != "fds" || m.Count != 8000 || m.Max != 10000 || m.UsedPct != 80 {
		t.Fatalf("countMeter = %+v, want fds 8000/10000 80%%", m)
	}
	// An unknown limit yields no meter rather than a divide-by-zero.
	if countMeter("pids", 100, 0) != nil {
		t.Fatal("max == 0 must produce no meter")
	}
}

func TestByteUsage(t *testing.T) {
	used, total, free, ok := byteUsage(metrics.Reading{Absolute: 3, Total: 8, HasTotal: true})
	if !ok || used != 3 || total != 8 || free != 5 {
		t.Fatalf("got used=%d total=%d free=%d ok=%v, want 3/8/5/true", used, total, free, ok)
	}
	// used above total must clamp free to 0, never underflow the unsigned subtraction.
	if _, _, free, ok := byteUsage(metrics.Reading{Absolute: 10, Total: 8, HasTotal: true}); !ok || free != 0 {
		t.Fatalf("over-capacity free = %d ok=%v, want 0/true", free, ok)
	}
	// No capacity (missing metric / no total) reports not-ok.
	if _, _, _, ok := byteUsage(metrics.Reading{Absolute: 5}); ok {
		t.Fatal("a reading with no total must report ok=false")
	}
}

func TestHostMetricBytesUnitAndPassthrough(t *testing.T) {
	mem := hostMetric("total_memory", metrics.Reading{
		Absolute: 4, Percent: 50, Total: 8,
		HasAbsolute: true, HasPercent: true, HasTotal: true, Ready: true,
	})
	if mem.Unit != "bytes" {
		t.Fatalf("memory unit = %q, want bytes", mem.Unit)
	}
	if mem.Percent != 50 || mem.Absolute != 4 || mem.Total != 8 || !mem.Ready {
		t.Fatalf("passthrough wrong: %+v", mem)
	}
	// A metric outside the special set is mapped plainly: no unit, no enrichment.
	cpu := hostMetric("total_cpu", metrics.Reading{Percent: 12, HasPercent: true, Ready: true})
	if cpu.Unit != "" || cpu.Total != 0 || cpu.Percent != 12 {
		t.Fatalf("total_cpu should be a plain passthrough: %+v", cpu)
	}
}
