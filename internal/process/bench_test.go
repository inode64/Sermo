package process

import "testing"

// BenchmarkSnapshot measures one full /proc identity scan — the shared
// per-cycle discovery cost — against the live host process table.
func BenchmarkSnapshot(b *testing.B) {
	reader := OSReader{}
	var n int
	for b.Loop() {
		snapshot, _ := Snapshot(reader)
		n = len(snapshot)
	}
	b.ReportMetric(float64(n), "procs")
}
