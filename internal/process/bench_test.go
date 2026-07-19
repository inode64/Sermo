package process

import "testing"

// BenchmarkBuildSnapshot measures one full /proc identity scan — the shared
// per-cycle discovery cost — against the live host process table.
func BenchmarkBuildSnapshot(b *testing.B) {
	reader := OSReader{}
	var n int
	for b.Loop() {
		n = len(buildSnapshot(reader))
	}
	b.ReportMetric(float64(n), "procs")
}
