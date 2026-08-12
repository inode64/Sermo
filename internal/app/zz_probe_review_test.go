package app

import (
	"testing"
	"time"
)

// Probe A: first sample of a PID had an unreadable start time (StartTicks == 0),
// the next sample reads it fine -> spurious `gone`?
func TestProbeSpuriousGone(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	unreadable := ProcInfo{PID: 42}
	readable := ProcInfo{PID: 42, StartTicks: 500, StartTime: now.Add(-time.Hour)}
	h := &procHarness{clock: now}
	s := &fakeProcSampler{cycles: [][]ProcInfo{{unreadable}, {readable}, {readable}}}
	w := h.watcher(procCond{onGone: true}, s)
	h.tick(w, 0)
	h.tick(w, 30*time.Second)
	h.tick(w, 30*time.Second)
	t.Logf("A: fired=%d", len(h.fired))
	for i, e := range h.fired {
		t.Logf("   [%d] change=%q age=%q", i, e[sermoEnvChange], e[sermoEnvAgeSeconds])
	}
}

// Probe B: same transient failure, but with a presence condition that is already
// true -> does the edge state reset and re-fire the hook (and `then.kill`)?
func TestProbeDuplicateFireAfterTransientStartFailure(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	unreadable := ProcInfo{PID: 42, RSS: 900}
	readable := ProcInfo{PID: 42, RSS: 900, StartTicks: 500, StartTime: now.Add(-time.Hour)}
	h := &procHarness{clock: now}
	s := &fakeProcSampler{cycles: [][]ProcInfo{{unreadable}, {readable}, {readable}}}
	w := h.watcher(procCond{memOp: ">", memValue: 100}, s)
	h.tick(w, 0)
	h.tick(w, 30*time.Second)
	h.tick(w, 30*time.Second)
	t.Logf("B: fired=%d (want 1: edge-triggered)", len(h.fired))
	for i, e := range h.fired {
		t.Logf("   [%d] change=%q age=%q", i, e[sermoEnvChange], e[sermoEnvAgeSeconds])
	}
}

// Probe C: wall-clock step forward. st.startTime is pinned to the first reading
// and never refreshed, while every later sample carries a re-anchored value.
// Does the reported age (and `for`) skew by the whole step?
func TestProbeClockStepSkewsAge(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	const step = 2 * time.Hour
	// The process really started 10 minutes ago and never restarts.
	before := ProcInfo{PID: 42, StartTicks: 500, StartTime: now.Add(-10 * time.Minute)}
	// After the clock steps forward 2h, btime is re-anchored: the kernel now
	// reports the same process as having started (now+step)-10m.
	after := ProcInfo{PID: 42, StartTicks: 500, StartTime: now.Add(step - 10*time.Minute)}
	h := &procHarness{clock: now}
	s := &fakeProcSampler{cycles: [][]ProcInfo{{before}, {after}, {after}}}
	// for: 120m + a kill would SIGTERM here.
	w := h.watcher(procCond{minAge: 120 * time.Minute}, s)
	h.tick(w, 0)
	h.clock = h.clock.Add(step) // the wall clock steps forward
	h.tick(w, 0)
	t.Logf("C: fired=%d (want 0: the process is only 10m old)", len(h.fired))
	for i, e := range h.fired {
		t.Logf("   [%d] change=%q age=%q", i, e[sermoEnvChange], e[sermoEnvAgeSeconds])
	}
	if st := w.state[42]; st != nil {
		t.Logf("   pinned startTime=%v, sample startTime=%v, age=%v", st.startTime, after.StartTime, st.age(h.clock))
	}
}

// Probe D: PID 1 / a process started in the very first jiffies has starttime 0
// ticks -> StartTicks == 0 is indistinguishable from "unreadable".
func TestProbeZeroTicksProcess(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	// systemd: starttime == 0 ticks, StartTime == boot time (readable, non-zero).
	p := ProcInfo{PID: 1, StartTicks: 0, StartTime: now.Add(-72 * time.Hour)}
	h := &procHarness{clock: now}
	s := &fakeProcSampler{cycles: [][]ProcInfo{{p}, {p}}}
	w := h.watcher(procCond{onGone: true}, s)
	h.tick(w, 0)
	h.tick(w, 30*time.Second)
	t.Logf("D: fired=%d; supersededBy(zero-ticks sample) never triggers, so a PID-1 replacement is undetectable", len(h.fired))
}
