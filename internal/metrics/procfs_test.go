package metrics

import (
	"os"
	"runtime"
	"testing"
)

// TestOSReaderProcfs exercises the real /proc readers. It is Linux-only (the
// procfs layout it parses does not exist elsewhere).
func TestOSReaderProcfs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("OSReader reads Linux /proc")
	}
	r := OSReader{}

	if total, used, ok := r.TotalMemory(); !ok || total == 0 || used > total {
		t.Errorf("TotalMemory = (%d, %d, %v); want ok with 0 < used <= total", total, used, ok)
	}
	if busy, total, ok := r.SystemCPU(); !ok || total == 0 || busy > total {
		t.Errorf("SystemCPU = (%d, %d, %v); want ok with busy <= total", busy, total, ok)
	}
	if l1, l5, l15, ok := r.LoadAverages(); !ok || l1 < 0 || l5 < 0 || l15 < 0 {
		t.Errorf("LoadAverages = (%v, %v, %v, %v); want ok with non-negative values", l1, l5, l15, ok)
	}
	// Swap may be absent; when reported, used must not exceed total.
	if total, used, ok := r.TotalSwap(); ok && used > total {
		t.Errorf("TotalSwap used %d > total %d", used, total)
	}
	if total, used, swapTotal, swapUsed, ok, swapOK := r.TotalMemoryAndSwap(); !ok || total == 0 || used > total || (swapOK && swapUsed > swapTotal) {
		t.Errorf("TotalMemoryAndSwap = (%d, %d, %d, %d, %v, %v); want valid memory and optional valid swap", total, used, swapTotal, swapUsed, ok, swapOK)
	}
	if n := r.NumCPU(); n < 1 {
		t.Errorf("NumCPU = %d, want >= 1", n)
	}
	if hz := r.ClockTicks(); hz <= 0 {
		t.Errorf("ClockTicks = %v, want > 0", hz)
	}

	pid := os.Getpid()
	if _, ok := r.ProcessCPU(pid); !ok {
		t.Error("ProcessCPU(self) not ok")
	}
	if rss, ok := r.ProcessRSS(pid); !ok || rss == 0 {
		t.Errorf("ProcessRSS(self) = (%d, %v); want ok with rss > 0", rss, ok)
	}
	// VmSwap is usually 0 for the test process, but the read must succeed.
	if _, ok := r.ProcessSwap(pid); !ok {
		t.Error("ProcessSwap(self) not ok")
	}
	// read/write bytes may legitimately be 0; we only require the file to parse.
	if _, _, ok := r.ProcessIO(pid); !ok {
		t.Error("ProcessIO(self) not ok")
	}
}

func TestParseProcMeminfoTotals(t *testing.T) {
	data := []byte("MemTotal:       1000 kB\nMemAvailable:    250 kB\nSwapTotal:       2000 kB\nSwapFree:        500 kB\n")
	totals := parseProcMeminfoTotals(data)
	if !totals.memoryOK || totals.memoryTotal != 1000*1024 || totals.memoryUsed != 750*1024 {
		t.Fatalf("memory totals = %+v, want 1000k total and 750k used", totals)
	}
	if !totals.swapOK || totals.swapTotal != 2000*1024 || totals.swapUsed != 1500*1024 {
		t.Fatalf("swap totals = %+v, want 2000k total and 1500k used", totals)
	}
}

func TestParseProcMeminfoTotalsNoSwapDevice(t *testing.T) {
	data := []byte("MemTotal:       1000 kB\nMemAvailable:    250 kB\nSwapTotal:          0 kB\nSwapFree:           0 kB\n")
	totals := parseProcMeminfoTotals(data)
	if !totals.memoryOK {
		t.Fatalf("memory totals = %+v, want valid memory", totals)
	}
	if !totals.swapOK || totals.swapTotal != 0 || totals.swapUsed != 0 {
		t.Fatalf("swap totals = %+v, want valid zero-swap totals", totals)
	}
}

func TestParseMeminfoKBRejectsMissingValue(t *testing.T) {
	if got, ok := parseMeminfoKB("VmSwap:"); ok || got != 0 {
		t.Fatalf("parseMeminfoKB(missing value) = (%d, %v), want (0, false)", got, ok)
	}
}

// swapReader adds an optional TotalSwap to fakeReader so SampleSystem's swap
// branch can be exercised deterministically.
type swapReader struct {
	fakeReader
	swapTotal, swapUsed uint64
	swapOK              bool
}

func (r swapReader) TotalSwap() (uint64, uint64, bool) {
	return r.swapTotal, r.swapUsed, r.swapOK
}

func TestSampleSystemSwap(t *testing.T) {
	r := swapReader{
		fakeReader: fakeReader{memTotal: 1000, memUsed: 250, hz: 100, ncpu: 1},
		swapTotal:  2000, swapUsed: 500, swapOK: true,
	}
	snap := New(r).SampleSystem()
	sw, ok := snap["total_swap"]
	if !ok {
		t.Fatal("total_swap missing from snapshot")
	}
	if sw.Absolute != 500 || !sw.HasPercent || sw.Percent != 25 {
		t.Errorf("total_swap = %+v, want 500 bytes / 25%%", sw)
	}
	if !sw.HasTotal || sw.Total != 2000 {
		t.Errorf("total_swap capacity = %+v, want Total 2000 (the UI derives free from it)", sw)
	}
}

func TestSampleSystemNoSwapDevice(t *testing.T) {
	// total == 0 means no swap device: the metric must be omitted entirely.
	r := swapReader{
		fakeReader: fakeReader{memTotal: 1000, hz: 100, ncpu: 1},
		swapOK:     true, // reader works, but reports zero total
	}
	snap := New(r).SampleSystem()
	if _, ok := snap["total_swap"]; ok {
		t.Error("total_swap should be absent when there is no swap device")
	}
}

func TestParseProcMeminfoTotalsAvailableEqualsTotal(t *testing.T) {
	// MemAvailable == MemTotal is a legitimate fully-free host (0 used), not the
	// rejected available > total case.
	data := []byte("MemTotal:       1000 kB\nMemAvailable:   1000 kB\n")
	totals := parseProcMeminfoTotals(data)
	if !totals.memoryOK || totals.memoryTotal != 1000*1024 || totals.memoryUsed != 0 {
		t.Fatalf("equal mem totals = %+v, want valid memory with 0 used", totals)
	}
}
