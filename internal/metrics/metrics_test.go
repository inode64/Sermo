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
	if got, _ := Compare(pct, "<", "40%"); got {
		t.Error("45% < 40% should be false")
	}

	abs := Reading{Absolute: 1500, HasAbsolute: true, Ready: true}
	if got, _ := Compare(abs, ">=", "1500"); !got {
		t.Error("1500 >= 1500 should be true")
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

func (r fakeReader) ProcessCPU(pid int) (uint64, bool) { v, ok := r.cpu[pid]; return v, ok }
func (r fakeReader) ProcessRSS(pid int) (uint64, bool) { v, ok := r.rss[pid]; return v, ok }
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
