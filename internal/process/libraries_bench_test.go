package process

import "testing"

// BenchmarkScanLibrariesHost measures one host-wide stale-library scan, the
// stale_libraries check's per-minute cost, against the live process table:
// every readable process's mapped libraries are compared with the installed
// files.
func BenchmarkScanLibrariesHost(b *testing.B) {
	var processes int
	for b.Loop() {
		report, _ := ScanLibraries(b.Context(), nil)
		processes = report.Processes
	}
	b.ReportMetric(float64(processes), "procs")
}
