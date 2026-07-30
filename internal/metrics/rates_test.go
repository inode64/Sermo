package metrics

import (
	"testing"
	"time"
)

// These cover the pure rate computations (ioRate, cpuRate, perProcCPURates,
// maxProcCPURate): the exact value on a normal delta, the counter-reset clamp
// to 0, and the non-positive wall/hz/ncpu guards. Mutation testing flagged the
// arithmetic and boundary conditions here as covered-but-unasserted.

func TestIORate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t2 := t0.Add(2 * time.Second)

	if r := ioRate(100, 300, t0, t2); !r.Ready || !r.HasAbsolute || r.Absolute != 100 {
		t.Errorf("normal: got %+v, want rate 100 ready", r)
	}
	// Counter reset (cur < prev) and the cur==prev boundary both clamp to 0.
	if r := ioRate(300, 100, t0, t2); !r.Ready || r.Absolute != 0 {
		t.Errorf("reset: got %+v, want rate 0 ready", r)
	}
	if r := ioRate(100, 100, t0, t2); !r.Ready || r.Absolute != 0 {
		t.Errorf("equal: got %+v, want rate 0 ready", r)
	}
	// Non-positive wall is not ready (zero is the boundary, negative is reversed).
	if r := ioRate(100, 300, t0, t0); r.Ready {
		t.Errorf("wall==0: got %+v, want not ready", r)
	}
	if r := ioRate(100, 300, t2, t0); r.Ready {
		t.Errorf("wall<0: got %+v, want not ready", r)
	}
}

func TestCPURate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t2 := t0.Add(2 * time.Second)
	// Non-zero prev so the Δ (300-100=200) differs from the sum, pinning the
	// subtraction. /100 hz = 2 cpu-seconds over 2 wall-seconds, 1 cpu => 100%.
	prev := cpuSample{ticks: 100, at: t0}
	cur := cpuSample{ticks: 300, at: t2}

	if r := cpuRate(prev, cur, 100, 1); !r.Ready || r.Percent != 100 {
		t.Errorf("normal: got %+v, want 100%% ready", r)
	}
	// Counter reset clamps to 0% but stays ready.
	if r := cpuRate(cpuSample{ticks: 200, at: t0}, cpuSample{ticks: 0, at: t2}, 100, 1); !r.Ready || r.Percent != 0 {
		t.Errorf("reset: got %+v, want 0%% ready", r)
	}
	// Each non-positive guard returns not-ready (these are div-by-zero traps).
	for _, bad := range []struct {
		name string
		r    Reading
	}{
		{"wall<=0", cpuRate(prev, cpuSample{ticks: 200, at: t0}, 100, 1)},
		{"ncpu<=0", cpuRate(prev, cur, 100, 0)},
		{"hz<=0", cpuRate(prev, cur, 0, 1)},
	} {
		if bad.r.Ready {
			t.Errorf("%s: got %+v, want not ready", bad.name, bad.r)
		}
	}
}

func TestPerProcCPURates(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t2 := t0.Add(2 * time.Second)
	// Non-zero prev for pid1 (Δ=200≠sum), a reset pid, a no-prev pid, and a
	// pid whose count is unchanged (the curT==prevT boundary: rate 0, kept).
	prev := procCPUSample{ticks: map[int]uint64{1: 50, 2: 100, 4: 100}, at: t0}
	cur := procCPUSample{ticks: map[int]uint64{1: 250, 2: 50, 3: 10, 4: 100}, at: t2}

	rates, ready := perProcCPURates(prev, cur, 100)
	if !ready {
		t.Fatalf("want ready")
	}
	if rates[1] != 100 { // (250-50)/100/2*100
		t.Errorf("pid1 rate = %v, want 100", rates[1])
	}
	if _, ok := rates[2]; ok { // cur<prev (counter reset) is skipped
		t.Errorf("pid2 reset should be skipped, got %v", rates[2])
	}
	if _, ok := rates[3]; ok { // pid absent from prev is skipped
		t.Errorf("pid3 (no prev) should be skipped, got %v", rates[3])
	}
	if r, ok := rates[4]; !ok || r != 0 { // curT==prevT: kept at rate 0, not skipped
		t.Errorf("pid4 unchanged = (%v,%v), want (0,true)", r, ok)
	}

	// No previous sample, and non-positive wall/hz, all report not-ready.
	if _, ok := perProcCPURates(procCPUSample{}, cur, 100); ok {
		t.Errorf("nil prev ticks: want not ready")
	}
	if _, ok := perProcCPURates(prev, procCPUSample{ticks: cur.ticks, at: t0}, 100); ok {
		t.Errorf("wall<=0: want not ready")
	}
	if _, ok := perProcCPURates(prev, cur, 0); ok {
		t.Errorf("hz<=0: want not ready")
	}
}

// TestMaxCoreRates pins the distinction the metric exists for: pid 2 burns 300% of
// one core, but spread over three equally busy threads no single core gave it more
// than 100%. Reporting 300% — what a per-process maximum does — describes a core that
// cannot exist.
func TestMaxCoreRates(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t2 := t0.Add(1 * time.Second)
	prev := procCPUSample{
		ticks: map[int]uint64{1: 0, 2: 0},
		threadTicks: map[int]map[int]uint64{
			2: {21: 0, 22: 0, 23: 0},
		},
		at: t0,
	}
	cur := procCPUSample{
		ticks: map[int]uint64{1: 100, 2: 300},
		threadTicks: map[int]map[int]uint64{
			2: {21: 100, 22: 100, 23: 100},
		},
		at: t2,
	}
	procRates, ready := perProcCPURates(prev, cur, 100)
	if !ready {
		t.Fatal("per-process rates: want ready")
	}

	values, exact := maxCoreRates(prev, cur, 100, procRates)
	// pid 1 was never thread-sampled, so its own rate stands in as the bound — which
	// for a single-threaded process is also the exact answer.
	if values[1] != 100 || exact[1] {
		t.Errorf("pid 1 (unsampled): got %v exact=%v, want 100%% bounded", values[1], exact[1])
	}
	// A zero bound is exact: no thread of an idle process used a core.
	zero, zeroExact := maxCoreRates(prev, cur, 100, map[int]float64{9: 0})
	if zero[9] != 0 || !zeroExact[9] {
		t.Errorf("idle pid: got %v exact=%v, want an exact 0%%", zero[9], zeroExact[9])
	}
	if values[2] != 100 || !exact[2] {
		t.Errorf("pid 2 (3 threads x 100%%): got %v exact=%v, want a measured 100%%", values[2], exact[2])
	}
	if r := maxCoreReading(values); !r.Ready || r.Percent != 100 {
		t.Errorf("aggregate: got %+v, want 100%% ready", r)
	}
	// The no-previous-sample case belongs to sampleMaxCore, which returns before
	// reaching here; TestCPUThreadMeasuresBusiestThread covers it on its first cycle.
}

// TestMaxThreadRateSkipsUnusableThreads covers the tid bookkeeping: a thread absent
// from the previous sample cannot yield a delta, and a counter that went backwards
// means the tid was recycled onto a different thread.
func TestMaxThreadRateSkipsUnusableThreads(t *testing.T) {
	for _, tc := range []struct {
		name      string
		prev, cur map[int]uint64
		wantRate  float64
		wantOK    bool
	}{
		{"new tid ignored", map[int]uint64{1: 0}, map[int]uint64{1: 50, 2: 900}, 50, true},
		{"recycled tid ignored", map[int]uint64{1: 0, 2: 500}, map[int]uint64{1: 50, 2: 10}, 50, true},
		{"no prev sample", nil, map[int]uint64{1: 50}, 0, false},
		{"no cur sample", map[int]uint64{1: 0}, nil, 0, false},
		{"no usable tid", map[int]uint64{1: 0}, map[int]uint64{2: 50}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rate, ok := maxThreadRate(tc.prev, tc.cur, 100, 1)
			if ok != tc.wantOK || rate != tc.wantRate {
				t.Errorf("got %v ok=%v, want %v ok=%v", rate, ok, tc.wantRate, tc.wantOK)
			}
		})
	}
}

// TestThreadSampleTargets pins the floor: it is what keeps an idle container runtime
// from paying one file read per thread, every cycle.
func TestThreadSampleTargets(t *testing.T) {
	threads := map[int]uint64{1: 1, 2: 800, 3: 800, 4: 800}
	rates := map[int]float64{
		1: 90,                              // single-threaded: never worth reading
		2: 0.6,                             // idle-but-huge: below the floor
		3: CPUThreadSampleFloorPercent,     // exactly at the floor: sampled
		4: CPUThreadSampleFloorPercent / 2, // under the floor: bounded, whatever it did before
	}

	got := threadSampleTargets(rates, threads)
	want := map[int]bool{3: true}
	if len(got) != len(want) {
		t.Fatalf("targets: got %v, want pids %v", got, want)
	}
	for _, pid := range got {
		if !want[pid] {
			t.Errorf("pid %d sampled but should not be", pid)
		}
	}
}
