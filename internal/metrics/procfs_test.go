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
