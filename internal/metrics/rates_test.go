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

func TestMaxProcCPURate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	t2 := t0.Add(1 * time.Second)
	prev := procCPUSample{ticks: map[int]uint64{1: 0, 2: 0}, at: t0}
	cur := procCPUSample{ticks: map[int]uint64{1: 100, 2: 300}, at: t2}

	if r := maxProcCPURate(prev, cur, 100); !r.Ready || r.Percent != 300 {
		t.Errorf("peak: got %+v, want 300%% ready", r)
	}
	// Not ready without a previous sample.
	if r := maxProcCPURate(procCPUSample{}, cur, 100); r.Ready {
		t.Errorf("no prev: got %+v, want not ready", r)
	}
}
