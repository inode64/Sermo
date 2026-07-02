package app

import (
	"context"
	"strings"
	"syscall"
	"testing"
	"time"

	"sermo/internal/process"
)

// fakeSignaler records the signals a kill action would deliver instead of
// touching real processes.
type fakeSignaler struct {
	sent []sigCall
	err  error
}

type sigCall struct {
	pid int
	sig syscall.Signal
}

func (f *fakeSignaler) Signal(pid int, sig syscall.Signal) error {
	f.sent = append(f.sent, sigCall{pid, sig})
	return f.err
}

// fixedSampler always reports the same match set — used where a test does not
// care about per-cycle scripting (e.g. the escalation re-sample).
type fixedSampler struct {
	infos []ProcInfo
	ok    bool
}

func (f fixedSampler) Sample(ProcMatch) ([]ProcInfo, bool) { return f.infos, f.ok }

func killProcInfo() ProcInfo {
	return ProcInfo{PID: 42, User: "root", UID: 0, Exe: "/usr/bin/sudo", ExeOK: true}
}

// killWatcher builds a process watch whose only action is a native kill. The PID
// is pre-seeded as first-seen an hour ago so a minAge condition fires on the very
// first cycle, keeping the kill assertions independent of age bookkeeping.
func killWatcher(h *procHarness, ks *killSpec, sig *fakeSignaler, sampler ProcSampler, onGone, dryRun bool) *procWatcher {
	ks.selector = process.KillSelector{Users: []string{"root"}, ExeAny: []string{"/usr/bin/sudo"}}
	w := &procWatcher{
		name:     "pw",
		match:    ProcMatch{Name: "/usr/bin/sudo", User: "root"},
		cond:     procCond{minAge: time.Second, onGone: onGone},
		kill:     ks,
		signaler: sig,
		resolve: func(user string) (uint32, bool) {
			if user == "root" {
				return 0, true
			}
			return 0, false
		},
		sleep:   func(time.Duration) {}, // never wait for real in tests
		dryRun:  dryRun,
		emit:    func(e Event) { h.events = append(h.events, e) },
		now:     func() time.Time { return h.clock },
		sampler: sampler,
	}
	w.state = map[int]*procState{42: {firstSeen: h.clock.Add(-time.Hour)}}
	return w
}

func killEvents(events []Event) []Event {
	var out []Event
	for _, e := range events {
		if strings.HasPrefix(e.Kind, "kill") {
			out = append(out, e)
		}
	}
	return out
}

func TestProcWatchKillSendsSIGTERM(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	sig := &fakeSignaler{}
	w := killWatcher(h, &killSpec{signal: syscall.SIGTERM}, sig,
		fixedSampler{infos: []ProcInfo{killProcInfo()}, ok: true}, false, false)

	w.runCycle(context.Background())

	if len(sig.sent) != 1 || sig.sent[0] != (sigCall{42, syscall.SIGTERM}) {
		t.Fatalf("want one SIGTERM to pid 42, got %v", sig.sent)
	}
	ev := killEvents(h.events)
	if len(ev) != 1 || ev[0].Kind != "kill" {
		t.Fatalf("want one kill event, got %v", h.events)
	}
	// Edge-triggered: a second cycle with the same PID must not re-signal.
	w.runCycle(context.Background())
	if len(sig.sent) != 1 {
		t.Fatalf("kill re-fired on a steady PID: %v", sig.sent)
	}
}

func TestProcWatchKillGoneDoesNotSignal(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	sig := &fakeSignaler{}
	// Seen present, then gone. A `gone` fire has nothing to kill.
	s := &fakeProcSampler{cycles: [][]ProcInfo{{killProcInfo()}, {}}}
	w := killWatcher(h, &killSpec{signal: syscall.SIGTERM}, sig, s, true, false)
	// Reset the pre-seeded state so the PID is genuinely first seen this run.
	w.state = map[int]*procState{}

	w.runCycle(context.Background()) // present
	w.runCycle(context.Background()) // gone -> gone fire, no kill

	if len(sig.sent) != 0 {
		t.Fatalf("gone fire signalled a process: %v", sig.sent)
	}
}

func TestProcWatchKillDryRunDoesNotSignal(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	sig := &fakeSignaler{}
	w := killWatcher(h, &killSpec{signal: syscall.SIGTERM}, sig,
		fixedSampler{infos: []ProcInfo{killProcInfo()}, ok: true}, false, true)

	w.runCycle(context.Background())

	if len(sig.sent) != 0 {
		t.Fatalf("dry-run signalled a process: %v", sig.sent)
	}
	var dry *Event
	for i := range h.events {
		if h.events[i].Kind == "dry-run" {
			dry = &h.events[i]
		}
	}
	if dry == nil || !strings.Contains(dry.Message, "kill") {
		t.Fatalf("want a dry-run event mentioning kill, got %v", h.events)
	}
}

func TestProcWatchKillEscalatesToSIGKILL(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	sig := &fakeSignaler{}
	ks := &killSpec{signal: syscall.SIGTERM, escalate: true, termTimeout: 10 * time.Second, killTimeout: 5 * time.Second}
	// Present through the cycle and the post-TERM re-check, gone after SIGKILL.
	s := &fakeProcSampler{cycles: [][]ProcInfo{{killProcInfo()}, {killProcInfo()}, {}}}
	w := killWatcher(h, ks, sig, s, false, false)

	w.runCycle(context.Background())

	want := []sigCall{{42, syscall.SIGTERM}, {42, syscall.SIGKILL}}
	if len(sig.sent) != 2 || sig.sent[0] != want[0] || sig.sent[1] != want[1] {
		t.Fatalf("want SIGTERM then SIGKILL to pid 42, got %v", sig.sent)
	}
	for _, e := range killEvents(h.events) {
		if e.Kind == "kill-failed" {
			t.Fatalf("unexpected kill-failed after the process exited: %v", e)
		}
	}
}

func TestProcWatchKillEscalateStopsWhenGone(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	sig := &fakeSignaler{}
	ks := &killSpec{signal: syscall.SIGTERM, escalate: true, termTimeout: 10 * time.Second}
	// Cycle sampling sees the PID; the escalation re-sample sees it gone.
	s := &fakeProcSampler{cycles: [][]ProcInfo{{killProcInfo()}, {}}}
	w := killWatcher(h, ks, sig, s, false, false)

	w.runCycle(context.Background())

	if len(sig.sent) != 1 || sig.sent[0].sig != syscall.SIGTERM {
		t.Fatalf("want only SIGTERM when the PID exits during the grace period, got %v", sig.sent)
	}
}

func TestProcWatchKillFailedEmitsEvent(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	sig := &fakeSignaler{err: syscall.EPERM}
	w := killWatcher(h, &killSpec{signal: syscall.SIGTERM}, sig,
		fixedSampler{infos: []ProcInfo{killProcInfo()}, ok: true}, false, false)

	w.runCycle(context.Background())

	ev := killEvents(h.events)
	if len(ev) != 1 || ev[0].Kind != "kill-failed" {
		t.Fatalf("want a kill-failed event, got %v", h.events)
	}
}

func TestProcWatchKillSelectorMismatchDoesNotSignal(t *testing.T) {
	h := &procHarness{clock: time.Unix(1_000_000, 0)}
	sig := &fakeSignaler{}
	info := killProcInfo()
	info.ExeOK = false
	w := killWatcher(h, &killSpec{signal: syscall.SIGTERM}, sig,
		fixedSampler{infos: []ProcInfo{info}, ok: true}, false, false)

	w.runCycle(context.Background())

	if len(sig.sent) != 0 {
		t.Fatalf("selector mismatch signalled a process: %v", sig.sent)
	}
	ev := killEvents(h.events)
	if len(ev) != 1 || ev[0].Kind != "kill-failed" {
		t.Fatalf("want a kill-failed event, got %v", h.events)
	}
}
