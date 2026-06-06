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
	cpu  map[int]uint64
	rss  map[int]uint64
	hz   float64
	ncpu int
	// system
	memTotal, memUsed uint64
	sysBusy, sysTotal uint64
}

func (r fakeReader) ProcessCPU(pid int) (uint64, bool) { v, ok := r.cpu[pid]; return v, ok }
func (r fakeReader) ProcessRSS(pid int) (uint64, bool) { v, ok := r.rss[pid]; return v, ok }
func (r fakeReader) TotalMemory() (uint64, uint64, bool) {
	return r.memTotal, r.memUsed, r.memTotal > 0
}
func (r fakeReader) SystemCPU() (uint64, uint64, bool)               { return r.sysBusy, r.sysTotal, true }
func (r fakeReader) LoadAverages() (float64, float64, float64, bool) { return 1.5, 0.7, 0.3, true }
func (r fakeReader) NumCPU() int                                     { return r.ncpu }
func (r fakeReader) ClockTicks() float64                             { return r.hz }

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
