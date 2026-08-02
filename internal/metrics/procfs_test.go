package metrics

import (
	"math"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestProcBootTimeValueRejectsUnsignedOverflow(t *testing.T) {
	tests := []struct {
		name string
		sec  uint64
		ok   bool
		want int64
	}{
		{name: "valid", sec: 123, ok: true, want: 123},
		{name: "largest signed value", sec: math.MaxInt64, ok: true, want: math.MaxInt64},
		{name: "unsigned overflow", sec: math.MaxInt64 + 1},
		{name: "missing field"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := procBootTimeValue(tc.sec, tc.ok)
			wantOK := tc.ok && tc.sec <= math.MaxInt64
			if ok != wantOK || got != tc.want {
				t.Fatalf("procBootTimeValue(%d, %v) = (%d, %v), want (%d, %v)", tc.sec, tc.ok, got, ok, tc.want, wantOK)
			}
		})
	}
}

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
	if count, ok := r.ProcessFDs(pid); !ok || count == 0 {
		t.Errorf("ProcessFDs(self) = (%d, %v); want ok with count > 0", count, ok)
	}
	if count, ok := r.ProcessThreads(pid); !ok || count == 0 {
		t.Errorf("ProcessThreads(self) = (%d, %v); want ok with count > 0", count, ok)
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

func TestParseProcIO(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		wantRead  uint64
		wantWrite uint64
		ok        bool
	}{
		{
			name:      "valid",
			data:      "rchar: 1\nread_bytes: 10\nwrite_bytes: 25\ncancelled_write_bytes: 0\n",
			wantRead:  10,
			wantWrite: 25,
			ok:        true,
		},
		{
			name: "missing write bytes",
			data: "read_bytes: 10\n",
			ok:   false,
		},
		{
			name: "malformed read bytes",
			data: "read_bytes: nope\nwrite_bytes: 25\n",
			ok:   false,
		},
		{
			name: "malformed write bytes",
			data: "read_bytes: 10\nwrite_bytes: nope\n",
			ok:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotRead, gotWrite, ok := parseProcIO(tc.data)
			if ok != tc.ok || gotRead != tc.wantRead || gotWrite != tc.wantWrite {
				t.Fatalf("parseProcIO() = (%d, %d, %v); want (%d, %d, %v)", gotRead, gotWrite, ok, tc.wantRead, tc.wantWrite, tc.ok)
			}
		})
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

// TestOSReaderProcessThreadCPU exercises the real /proc/<pid>/task reader against
// this test binary, whose Go runtime is genuinely multithreaded. It is the one part
// of the cpu_thread path that a fake Reader cannot cover: the procfs layout, the
// comm-splitting in each thread's stat, and that per-thread jiffies actually advance
// independently.
//
// It pins the property the metric exists for: one busy thread must stand out from its
// idle siblings. Comparing the peak against the average is what a per-process
// maximum could never show.
func TestOSReaderProcessThreadCPU(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("OSReader reads Linux /proc")
	}
	r := OSReader{}
	pid := os.Getpid()

	first, ok := r.ProcessThreadCPU(pid)
	if !ok {
		t.Fatalf("ProcessThreadCPU(%d) not ok; want this process's own threads", pid)
	}
	if len(first) < 2 {
		t.Skipf("runtime exposed only %d thread(s); nothing to distinguish", len(first))
	}

	// Pin one goroutine to its own OS thread and spin it, leaving the rest idle. The
	// spin is the point: this test needs one thread to accumulate jiffies that its
	// siblings do not, which is precisely what cpu_thread has to surface.
	var stop atomic.Bool
	spinning := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		close(spinning)
		for !stop.Load() {
			runtime.Gosched()
		}
	}()
	<-spinning
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		runtime.Gosched()
	}
	stop.Store(true)

	second, ok := r.ProcessThreadCPU(pid)
	if !ok {
		t.Fatal("second ProcessThreadCPU read failed")
	}

	var deltas []uint64
	var sum, peak uint64
	for tid, cur := range second {
		prev, seen := first[tid]
		if !seen || cur < prev {
			continue
		}
		d := cur - prev
		deltas = append(deltas, d)
		sum += d
		if d > peak {
			peak = d
		}
	}
	if len(deltas) < 2 {
		t.Fatalf("only %d thread(s) present in both samples; want at least 2", len(deltas))
	}
	if peak == 0 {
		t.Fatal("no thread accumulated CPU time across the two samples")
	}
	// The spinning thread must carry visibly more than an even split would give it.
	if avg := float64(sum) / float64(len(deltas)); float64(peak) <= avg {
		t.Errorf("peak thread delta %d not above the %v average across %d threads: a busy thread must stand out", peak, avg, len(deltas))
	}
}
