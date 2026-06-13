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
