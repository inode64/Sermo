package metrics

import (
	"testing"
	"time"
)

func TestCompare(t *testing.T) {
	pct := Reading{Percent: 45, HasPercent: true, Ready: true}
	if got, _ := Compare(pct, ">", "40%"); !got {
		t.Error("45% > 40% should be true")
	}
	if got, _ := Compare(pct, ">", "40 %"); !got {
		t.Error("45% > 40 % should be true")
	}
	if got, _ := Compare(pct, "<", "40%"); got {
		t.Error("45% < 40% should be false")
	}

	abs := Reading{Absolute: 1500, HasAbsolute: true, Ready: true}
	if got, _ := Compare(abs, ">=", "1500"); !got {
		t.Error("1500 >= 1500 should be true")
	}
	if got, _ := Compare(abs, ">=", " 1500 "); !got {
		t.Error("1500 >= 1500 with spaced threshold should be true")
	}
}

func TestCompareOperatorsAndErrors(t *testing.T) {
	r := Reading{Absolute: 50, HasAbsolute: true, Ready: true}
	for _, c := range []struct {
		op, thr string
		want    bool
	}{
		{"<=", "50", true}, {"<=", "49", false},
		{"==", "50", true}, {"==", "51", false},
		{"!=", "51", true}, {"!=", "50", false},
	} {
		if got, err := Compare(r, c.op, c.thr); err != nil || got != c.want {
			t.Errorf("Compare(50, %q, %q) = %v, %v; want %v", c.op, c.thr, got, err, c.want)
		}
	}
	if _, err := Compare(r, "=~", "50"); err == nil {
		t.Error("an unsupported metric operator must error")
	}
	if _, err := Compare(r, ">", "notanumber"); err == nil {
		t.Error("an invalid numeric threshold must error")
	}
}

func TestCompareNotReadyIsFalse(t *testing.T) {
	r := Reading{Percent: 99, HasPercent: true, Ready: false}
	if got, err := Compare(r, ">", "1%"); got || err != nil {
		t.Fatalf("not-ready reading must be false with no error, got %v/%v", got, err)
	}
}

func TestCompareFormMismatchErrors(t *testing.T) {
	// A percentage threshold against an absolute-only metric.
	r := Reading{Absolute: 5, HasAbsolute: true, Ready: true}
	if _, err := Compare(r, ">", "10%"); err == nil {
		t.Fatal("percentage threshold on an absolute-only metric should error")
	}
}

// fakeReader feeds scripted process/system values.
type fakeReader struct {
	cpu     map[int]uint64
	rss     map[int]uint64
	swap    map[int]uint64
	ioRead  map[int]uint64
	ioWrite map[int]uint64
	fds     map[int]uint64
	threads map[int]uint64
	hz      float64
	ncpu    int
	// system
	memTotal, memUsed uint64
	sysBusy, sysTotal uint64
}

func (r fakeReader) ProcessCPU(pid int) (uint64, bool)  { v, ok := r.cpu[pid]; return v, ok }
func (r fakeReader) ProcessRSS(pid int) (uint64, bool)  { v, ok := r.rss[pid]; return v, ok }
func (r fakeReader) ProcessSwap(pid int) (uint64, bool) { v, ok := r.swap[pid]; return v, ok }
func (r fakeReader) ProcessIO(pid int) (uint64, uint64, bool) {
	rd, ok := r.ioRead[pid]
	wr, ok2 := r.ioWrite[pid]
	return rd, wr, ok || ok2
}
func (r fakeReader) ProcessFDs(pid int) (uint64, bool)     { v, ok := r.fds[pid]; return v, ok }
func (r fakeReader) ProcessThreads(pid int) (uint64, bool) { v, ok := r.threads[pid]; return v, ok }
func (r fakeReader) TotalMemory() (uint64, uint64, bool) {
	return r.memTotal, r.memUsed, r.memTotal > 0
}
func (r fakeReader) SystemCPU() (uint64, uint64, bool)               { return r.sysBusy, r.sysTotal, true }
func (r fakeReader) LoadAverages() (float64, float64, float64, bool) { return 1.5, 0.7, 0.3, true }
func (r fakeReader) NumCPU() int                                     { return r.ncpu }
func (r fakeReader) ClockTicks() float64                             { return r.hz }

type combinedMemoryReader struct {
	fakeReader
	memoryTotal, memoryUsed uint64
	swapTotal, swapUsed     uint64
	memoryOK, swapOK        bool
	combinedCalls           int
	totalMemoryCalls        int
	totalSwapCalls          int
}

func (r *combinedMemoryReader) TotalMemory() (uint64, uint64, bool) {
	r.totalMemoryCalls++
	return r.memoryTotal, r.memoryUsed, r.memoryOK
}

func (r *combinedMemoryReader) TotalSwap() (uint64, uint64, bool) {
	r.totalSwapCalls++
	return r.swapTotal, r.swapUsed, r.swapOK
}

func (r *combinedMemoryReader) TotalMemoryAndSwap() (uint64, uint64, uint64, uint64, bool, bool) {
	r.combinedCalls++
	return r.memoryTotal, r.memoryUsed, r.swapTotal, r.swapUsed, r.memoryOK, r.swapOK
}

// readerNoSwap implements the core Reader interface but NOT the optional
// ProcessSwap, so the swap metric must not appear for it.
type readerNoSwap struct{}

func (readerNoSwap) ProcessCPU(int) (uint64, bool)        { return 0, false }
func (readerNoSwap) ProcessRSS(int) (uint64, bool)        { return 0, false }
func (readerNoSwap) ProcessIO(int) (uint64, uint64, bool) { return 0, 0, false }
func (readerNoSwap) ProcessFDs(int) (uint64, bool)        { return 0, false }
func (readerNoSwap) ProcessThreads(int) (uint64, bool)    { return 0, false }
func (readerNoSwap) TotalMemory() (uint64, uint64, bool)  { return 0, 0, false }
func (readerNoSwap) SystemCPU() (uint64, uint64, bool)    { return 0, 0, false }
func (readerNoSwap) LoadAverages() (float64, float64, float64, bool) {
	return 0, 0, 0, false
}
func (readerNoSwap) NumCPU() int         { return 1 }
func (readerNoSwap) ClockTicks() float64 { return 100 }

func TestServiceIOFDsThreadsAggregateOverTree(t *testing.T) {
	clock := time.Unix(0, 0)
	// Two processes (a service main + its child): io/fds/threads should sum.
	reader := fakeReader{
		ioRead:  map[int]uint64{10: 0, 20: 0},
		ioWrite: map[int]uint64{10: 0, 20: 0},
		fds:     map[int]uint64{10: 5, 20: 7},
		threads: map[int]uint64{10: 2, 20: 3},
		hz:      100, ncpu: 1,
	}
	c := New(reader)
	c.Now = func() time.Time { return clock }

	snap := c.SampleService("svc", []int{10, 20})
	if snap["fds"].Absolute != 12 {
		t.Fatalf("fds = %v, want 12 (5+7)", snap["fds"].Absolute)
	}
	if snap["threads"].Absolute != 5 {
		t.Fatalf("threads = %v, want 5 (2+3)", snap["threads"].Absolute)
	}
	if snap["io"].Ready {
		t.Fatal("io rate must not be ready on the first cycle")
	}

	// One second later: pid 10 read 1MB, pid 20 wrote 2MB -> io = 3MB/s.
	clock = clock.Add(time.Second)
	reader.ioRead[10] = 1 << 20
	reader.ioWrite[20] = 2 << 20
	c.Reader = reader
	snap = c.SampleService("svc", []int{10, 20})

	if !snap["io"].Ready || snap["io"].Absolute != float64(3<<20) {
		t.Fatalf("io = %+v, want 3MiB/s aggregated", snap["io"])
	}
	if snap["io_read"].Absolute != float64(1<<20) || snap["io_write"].Absolute != float64(2<<20) {
		t.Fatalf("io_read/io_write = %v/%v, want 1MiB/2MiB", snap["io_read"].Absolute, snap["io_write"].Absolute)
	}
}

func TestServiceIORateClampsOnShrink(t *testing.T) {
	clock := time.Unix(0, 0)
	reader := fakeReader{ioRead: map[int]uint64{10: 5000}, ioWrite: map[int]uint64{10: 0}, hz: 100, ncpu: 1}
	c := New(reader)
	c.Now = func() time.Time { return clock }
	c.SampleService("svc", []int{10}) // prime at 5000

	// A child exits / counter resets -> totals drop. Rate must clamp to 0, not underflow.
	clock = clock.Add(time.Second)
	reader.ioRead[10] = 1000
	c.Reader = reader
	snap := c.SampleService("svc", []int{10})
	if !snap["io"].Ready || snap["io"].Absolute != 0 {
		t.Fatalf("io on counter shrink = %+v, want clamped to 0", snap["io"])
	}
}

func TestServiceMemoryAndCount(t *testing.T) {
	reader := fakeReader{rss: map[int]uint64{10: 100, 20: 300}, memTotal: 1000, hz: 100, ncpu: 1}
	c := New(reader)
	snap := c.SampleService("svc", []int{10, 20})

	if snap["memory"].Absolute != 400 || !snap["memory"].HasPercent || snap["memory"].Percent != 40 {
		t.Fatalf("memory = %+v, want 400 bytes / 40%%", snap["memory"])
	}
	if snap["process_count"].Absolute != 2 {
		t.Fatalf("process_count = %v, want 2", snap["process_count"].Absolute)
	}
	// First cycle: cpu rate not ready.
	if snap["cpu"].Ready {
		t.Errorf("cpu must not be ready on the first cycle")
	}
}

func TestServiceGaugesNotReadyWhenAllReadsFail(t *testing.T) {
	// PIDs were discovered but every /proc read fails (they exited or are
	// unreadable). The summed gauges must be not-ready rather than a measured 0,
	// so a `fds < N` / `threads < N` threshold does not fire spuriously.
	reader := fakeReader{hz: 100, ncpu: 1} // all maps nil -> every read returns ok=false
	c := New(reader)
	snap := c.SampleService("svc", []int{10, 20})

	for _, name := range []string{"memory", "fds", "threads", "process_count"} {
		if snap[name].Ready {
			t.Errorf("%s must not be ready when no process could be read: %+v", name, snap[name])
		}
	}
	if snap["process_count"].Absolute != 0 {
		t.Fatalf("process_count = %v, want 0 found", snap["process_count"].Absolute)
	}
}

func TestServiceGaugesReadyZeroOnEmptyTree(t *testing.T) {
	// An empty process set is a genuine zero (the service has no processes), so
	// the gauges are ready, allowing a `process_count < 1` alert to fire.
	c := New(fakeReader{hz: 100, ncpu: 1})
	snap := c.SampleService("svc", nil)

	if !snap["process_count"].Ready || snap["process_count"].Absolute != 0 {
		t.Fatalf("process_count on empty tree = %+v, want ready 0", snap["process_count"])
	}
	if !snap["memory"].Ready || snap["memory"].Absolute != 0 {
		t.Fatalf("memory on empty tree = %+v, want ready 0", snap["memory"])
	}
}

func TestServiceProcessCountReflectsAlive(t *testing.T) {
	// Three PIDs handed in but only two are still readable; process_count must
	// report the two alive, not the three discovered.
	reader := fakeReader{rss: map[int]uint64{10: 100, 12: 50}, hz: 100, ncpu: 1}
	c := New(reader)
	snap := c.SampleService("svc", []int{10, 11, 12})
	if !snap["process_count"].Ready || snap["process_count"].Absolute != 2 {
		t.Fatalf("process_count = %+v, want ready 2 (11 exited)", snap["process_count"])
	}
}

func TestServiceSwapAggregatesOverTree(t *testing.T) {
	// Per-service swap must sum the parent and all children (like RSS), and report
	// its share of total swap. swapReader supplies the optional TotalSwap.
	reader := swapReader{
		fakeReader: fakeReader{
			swap: map[int]uint64{10: 100, 11: 200, 12: 50}, // parent + two children
			hz:   100, ncpu: 1,
		},
		swapTotal: 1000, swapOK: true,
	}
	c := New(reader)
	snap := c.SampleService("svc", []int{10, 11, 12})

	sw, ok := snap["swap"]
	if !ok {
		t.Fatal("a reader with ProcessSwap must produce a swap metric")
	}
	if sw.Absolute != 350 {
		t.Fatalf("swap absolute = %v, want 350 (sum across the tree)", sw.Absolute)
	}
	if !sw.HasPercent || sw.Percent != 35 {
		t.Fatalf("swap percent = %v (has=%v), want 35%% of total swap", sw.Percent, sw.HasPercent)
	}
}

func TestServiceSwapAbsentWithoutReaderSupport(t *testing.T) {
	// readerNoSwap omits ProcessSwap; the swap metric is then simply not produced.
	c := New(readerNoSwap{})
	if _, ok := c.SampleService("svc", []int{10})["swap"]; ok {
		t.Fatal("swap metric must be absent when the reader has no ProcessSwap")
	}
}

func TestSampleServiceUsesCombinedMemoryTotals(t *testing.T) {
	reader := &combinedMemoryReader{
		fakeReader: fakeReader{
			rss:  map[int]uint64{10: 100},
			swap: map[int]uint64{10: 50},
			hz:   100,
			ncpu: 1,
		},
		memoryTotal: 1000,
		memoryOK:    true,
		swapTotal:   200,
		swapOK:      true,
	}
	snap := New(reader).SampleService("svc", []int{10})

	if reader.combinedCalls != 1 || reader.totalMemoryCalls != 0 || reader.totalSwapCalls != 0 {
		t.Fatalf("memory calls combined/mem/swap = %d/%d/%d, want 1/0/0", reader.combinedCalls, reader.totalMemoryCalls, reader.totalSwapCalls)
	}
	if snap["memory"].Percent != 10 {
		t.Fatalf("memory percent = %v, want 10", snap["memory"].Percent)
	}
	if snap["swap"].Percent != 25 {
		t.Fatalf("swap percent = %v, want 25", snap["swap"].Percent)
	}
}

func TestServiceCPURate(t *testing.T) {
	clock := time.Unix(0, 0)
	reader := fakeReader{cpu: map[int]uint64{10: 0}, hz: 100, ncpu: 2}
	c := New(reader)
	c.Now = func() time.Time { return clock }

	c.SampleService("svc", []int{10}) // first sample: 0 ticks at t=0

	// One second later, the process used 100 ticks = 1 CPU-second. With 2 CPUs,
	// that is 1/(1*2)*100 = 50%.
	clock = clock.Add(time.Second)
	reader.cpu[10] = 100
	c.Reader = reader
	snap := c.SampleService("svc", []int{10})

	if !snap["cpu"].Ready {
		t.Fatal("cpu should be ready on the second cycle")
	}
	if got := snap["cpu"].Percent; got < 49.9 || got > 50.1 {
		t.Fatalf("cpu%% = %v, want ~50", got)
	}
}

func TestServiceCPURateClampsOnTickDrop(t *testing.T) {
	// When the tree's cumulative CPU ticks drop between cycles (a worker restarts,
	// or a busy PID is replaced by a fresh one), cur < prev. The rate must clamp
	// to 0, not underflow the unsigned subtraction into a bogus huge percentage.
	clock := time.Unix(0, 0)
	reader := fakeReader{cpu: map[int]uint64{10: 500}, hz: 100, ncpu: 2}
	c := New(reader)
	c.Now = func() time.Time { return clock }
	c.SampleService("svc", []int{10}) // baseline at 500 ticks

	clock = clock.Add(time.Second)
	reader.cpu[10] = 100 // dropped below the baseline
	c.Reader = reader
	snap := c.SampleService("svc", []int{10})
	if got := snap["cpu"].Percent; got != 0 {
		t.Fatalf("cpu%% on a tick drop = %v, want 0 (clamped)", got)
	}
	if !snap["cpu"].Ready {
		t.Fatal("cpu should still be ready after two samples")
	}
}

func TestSampleServiceCPUPerProcessAndAggregate(t *testing.T) {
	// SampleServiceCPU is the web-only path: it returns per-process single-core
	// rates plus the cpu_thread (max) and whole-machine aggregates, against the
	// previous call for the service.
	clock := time.Unix(0, 0)
	reader := fakeReader{cpu: map[int]uint64{10: 0, 11: 0, 12: 0}, hz: 100, ncpu: 4}
	c := New(reader)
	c.Now = func() time.Time { return clock }

	first := c.SampleServiceCPU("svc", []int{10, 11, 12})
	if first.CPU.Ready || first.CPUThread.Ready {
		t.Fatal("no rate on the first observation")
	}
	if len(first.PerProc) != 0 {
		t.Fatalf("PerProc should be empty on first cycle, got %v", first.PerProc)
	}

	// One second later: pid 11 used 80 ticks (~80% of one core), pid 10 used 20,
	// pid 12 idle.
	clock = clock.Add(time.Second)
	reader.cpu[10], reader.cpu[11], reader.cpu[12] = 20, 80, 0
	c.Reader = reader
	sc := c.SampleServiceCPU("svc", []int{10, 11, 12})

	if sc.NumCPU != 4 {
		t.Fatalf("NumCPU = %d, want 4", sc.NumCPU)
	}
	if got := sc.PerProc[11]; got < 79.9 || got > 80.1 {
		t.Fatalf("PerProc[11] = %v, want ~80 (single-core)", got)
	}
	if got := sc.PerProc[10]; got < 19.9 || got > 20.1 {
		t.Fatalf("PerProc[10] = %v, want ~20", got)
	}
	if got := sc.CPUThread.Percent; got < 79.9 || got > 80.1 {
		t.Fatalf("CPUThread = %v, want ~80 (busiest single process)", got)
	}
	// Whole-machine: 100 ticks = 1 CPU-second over 4 cores -> 25%.
	if got := sc.CPU.Percent; got < 24.9 || got > 25.1 {
		t.Fatalf("CPU (whole-machine) = %v, want ~25", got)
	}
}

func TestServiceCPUThreadMaxOverTree(t *testing.T) {
	// cpu_thread tracks the busiest single process against ONE thread, regardless
	// of how many CPUs the host has.
	clock := time.Unix(0, 0)
	reader := fakeReader{cpu: map[int]uint64{10: 0, 11: 0, 12: 0}, hz: 100, ncpu: 8}
	c := New(reader)
	c.Now = func() time.Time { return clock }

	snap := c.SampleService("svc", []int{10, 11, 12})
	if snap["cpu_thread"].Ready {
		t.Fatal("cpu_thread must not be ready on the first cycle")
	}

	// One second later: pid 11 used 95 ticks (~95% of one core), the others little.
	clock = clock.Add(time.Second)
	reader.cpu[10], reader.cpu[11], reader.cpu[12] = 10, 95, 5
	c.Reader = reader
	snap = c.SampleService("svc", []int{10, 11, 12})

	if !snap["cpu_thread"].Ready {
		t.Fatal("cpu_thread should be ready on the second cycle")
	}
	// Busiest process is pid 11 at ~95% of one thread — NOT diluted by the 8 cores
	// the way the whole-machine `cpu` metric would be.
	if got := snap["cpu_thread"].Percent; got < 94.9 || got > 95.1 {
		t.Fatalf("cpu_thread = %v, want ~95 (busiest single process on one thread)", got)
	}
	// The whole-machine cpu sums all three (110 ticks = 1.1s) over 8 cores ->
	// ~13.75%, which looks unremarkable even though one process is pegging a core.
	if got := snap["cpu"].Percent; got < 13.5 || got > 14 {
		t.Fatalf("cpu (whole-machine) = %v, want ~13.75", got)
	}
}

func TestCPUThreadCanExceed100ForMultithreaded(t *testing.T) {
	// A multi-threaded process accrues utime+stime across its threads, so its
	// single-thread rate can exceed 100% (it spans more than one core).
	clock := time.Unix(0, 0)
	reader := fakeReader{cpu: map[int]uint64{10: 0}, hz: 100, ncpu: 4}
	c := New(reader)
	c.Now = func() time.Time { return clock }
	c.SampleService("svc", []int{10})

	clock = clock.Add(time.Second)
	reader.cpu[10] = 250 // 2.5 cores' worth in one second
	c.Reader = reader
	snap := c.SampleService("svc", []int{10})
	if got := snap["cpu_thread"].Percent; got < 249.9 || got > 250.1 {
		t.Fatalf("cpu_thread = %v, want ~250", got)
	}
}

func TestServiceCPUAggregatesOverTree(t *testing.T) {
	// CPU% must sum the parent and all child processes of the service.
	clock := time.Unix(0, 0)
	reader := fakeReader{cpu: map[int]uint64{10: 0, 11: 0, 12: 0}, hz: 100, ncpu: 4}
	c := New(reader)
	c.Now = func() time.Time { return clock }

	c.SampleService("svc", []int{10, 11, 12}) // parent + two children, 0 ticks

	// One second later: parent +100, child +200, child +100 = 400 ticks =
	// 4 CPU-seconds. With 4 CPUs that is 4/(1*4)*100 = 100%.
	clock = clock.Add(time.Second)
	reader.cpu[10], reader.cpu[11], reader.cpu[12] = 100, 200, 100
	c.Reader = reader
	snap := c.SampleService("svc", []int{10, 11, 12})

	if !snap["cpu"].Ready {
		t.Fatal("cpu should be ready on the second cycle")
	}
	if got := snap["cpu"].Percent; got < 99.9 || got > 100.1 {
		t.Fatalf("cpu%% = %v, want ~100 (sum across the tree)", got)
	}
}

func TestCountCPULines(t *testing.T) {
	stat := "cpu  100 0 50 800 0 0 0 0 0 0\n" +
		"cpu0 25 0 12 200 0 0 0 0 0 0\n" +
		"cpu1 25 0 13 200 0 0 0 0 0 0\n" +
		"cpu2 25 0 13 200 0 0 0 0 0 0\n" +
		"cpu3 25 0 12 200 0 0 0 0 0 0\n" +
		"intr 12345\nctxt 67890\nbtime 1700000000\nprocesses 100\n"
	if n := countCPULines([]byte(stat)); n != 4 {
		t.Fatalf("countCPULines = %d, want 4 (the aggregate 'cpu' line is excluded)", n)
	}
	if n := countCPULines([]byte("cpu 1 2 3\n")); n != 0 {
		t.Fatalf("countCPULines with only the aggregate line = %d, want 0", n)
	}
}

func TestSampleSystemUsesCombinedMemoryTotals(t *testing.T) {
	reader := &combinedMemoryReader{
		fakeReader:  fakeReader{hz: 100, ncpu: 1},
		memoryTotal: 1000,
		memoryUsed:  250,
		memoryOK:    true,
		swapTotal:   2000,
		swapUsed:    500,
		swapOK:      true,
	}
	snap := New(reader).SampleSystem()

	if reader.combinedCalls != 1 || reader.totalMemoryCalls != 0 || reader.totalSwapCalls != 0 {
		t.Fatalf("memory calls combined/mem/swap = %d/%d/%d, want 1/0/0", reader.combinedCalls, reader.totalMemoryCalls, reader.totalSwapCalls)
	}
	if snap["total_memory"].Percent != 25 {
		t.Fatalf("total_memory percent = %v, want 25", snap["total_memory"].Percent)
	}
	if snap["total_swap"].Percent != 25 {
		t.Fatalf("total_swap percent = %v, want 25", snap["total_swap"].Percent)
	}
}

func TestSystemCPURateAndFreshness(t *testing.T) {
	clock := time.Unix(0, 0)
	reader := fakeReader{memTotal: 1000, memUsed: 250, sysBusy: 0, sysTotal: 0, hz: 100, ncpu: 1}
	c := New(reader)
	c.Now = func() time.Time { return clock }

	c.SampleSystem() // baseline

	clock = clock.Add(10 * time.Second)
	reader.sysBusy = 30
	reader.sysTotal = 100
	c.Reader = reader
	snap := c.SampleSystem()

	if got := snap["total_cpu"].Percent; got < 29.9 || got > 30.1 {
		t.Fatalf("total_cpu%% = %v, want ~30", got)
	}
	if snap["total_memory"].Percent != 25 {
		t.Fatalf("total_memory%% = %v, want 25", snap["total_memory"].Percent)
	}
	if snap["load1"].Absolute != 1.5 {
		t.Fatalf("load1 = %v, want 1.5", snap["load1"].Absolute)
	}

	// A second call within the freshness window returns the cached snapshot
	// (does not advance the rate baseline).
	reader.sysBusy = 999
	c.Reader = reader
	cached := c.SampleSystem()
	if cached["total_cpu"].Percent != snap["total_cpu"].Percent {
		t.Fatalf("system sample within freshness window should be cached")
	}
}

// A backward CPU counter (a reset) must not underflow into a bogus rate: the
// sample is simply not ready, like the per-process and IO rate samplers.
func TestSampleSystemCPUCounterReset(t *testing.T) {
	clock := time.Unix(0, 0)
	reader := fakeReader{memTotal: 1000, memUsed: 250, sysBusy: 100, sysTotal: 200, hz: 100, ncpu: 1}
	c := New(reader)
	c.Now = func() time.Time { return clock }
	c.SampleSystem() // baseline

	clock = clock.Add(time.Hour) // past the freshness window
	reader.sysBusy = 50          // counter went backward
	reader.sysTotal = 300
	c.Reader = reader
	r := c.SampleSystem()["total_cpu"]

	if r.Ready {
		t.Fatalf("total_cpu must not be ready after a backward counter, got Percent=%v", r.Percent)
	}
	if r.Percent != 0 {
		t.Fatalf("total_cpu%% = %v, want 0 (no underflow rate)", r.Percent)
	}
}
