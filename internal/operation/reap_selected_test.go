package operation

import (
	"context"
	"errors"
	"syscall"
	"testing"

	"sermo/internal/locks"
	"sermo/internal/process"
)

func selectedStray() process.Process {
	p := strayProc(300, "/usr/bin/dbus-daemon")
	p.StartTicks = 42
	p.Cgroup = "/service"
	return p
}

func selectedTarget(p process.Process) ReapTarget {
	return ReapTarget{PID: p.PID, StartTicks: p.StartTicks, Identity: process.SignalIdentity(p)}
}

func TestSelectedReapKeepsOtherProcesses(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "deleted"}[deleted], func(t *testing.T) {
			p := selectedStray()
			if deleted {
				p.ExePrev, p.Exe, p.ExeOK = p.Exe, "", false
				p.ExeFile = process.ExecutableFile{Device: 1, Inode: 99}
			}
			other := strayProc(400, "/usr/bin/dbus-daemon")
			h := defaultHarness()
			h.discoverSteps = [][]process.Process{{p, other}, {other}}
			signaler := &reapSignaler{}
			selector := dbusReapSelector()
			e := reapEngine(h, signaler, &selector)
			e.SessionExited = func(pid int, ticks uint64) (bool, error) { return pid == p.PID && ticks == p.StartTicks, nil }
			result := e.ReapProcess(context.Background(), selectedTarget(p))
			if !result.OK() || len(signaler.calls) != 1 || signaler.calls[0].pid != p.PID || signaler.calls[0].sig != syscall.SIGTERM {
				t.Fatalf("result=%+v signals=%v", result, signaler.calls)
			}
		})
	}
}

func TestSelectedReapRefusesUnsafeIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*process.Process, *ReapTarget, *Engine)
	}{
		{"missing policy", func(_ *process.Process, _ *ReapTarget, e *Engine) { e.ReapSelector = process.KillSelector{} }},
		{"reused pid", func(p *process.Process, _ *ReapTarget, _ *Engine) { p.StartTicks++ }},
		{"changed exe", func(p *process.Process, _ *ReapTarget, _ *Engine) { p.Exe = "/other" }},
		{"changed uid", func(p *process.Process, _ *ReapTarget, _ *Engine) { p.UID++ }},
		{"foreign cgroup", func(p *process.Process, _ *ReapTarget, _ *Engine) { p.Cgroup = "/other" }},
		{"no generation", func(_ *process.Process, target *ReapTarget, _ *Engine) { target.StartTicks = 0 }},
		{"not stray", func(p *process.Process, _ *ReapTarget, _ *Engine) { p.Stray = false }},
		{"delegated", func(p *process.Process, _ *ReapTarget, _ *Engine) { p.Delegated = true }},
		{"unverified deleted", func(p *process.Process, target *ReapTarget, _ *Engine) {
			p.ExePrev, p.Exe, p.ExeOK = p.Exe, "", false
			*target = selectedTarget(*p)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			p := selectedStray()
			target := selectedTarget(p)
			signaler := &reapSignaler{}
			selector := dbusReapSelector()
			e := reapEngine(h, signaler, &selector)
			tc.change(&p, &target, &e)
			h.discoverSteps = [][]process.Process{{p}}
			result := e.ReapProcess(context.Background(), target)
			if result.OK() || len(signaler.calls) != 0 {
				t.Fatalf("unsafe result=%+v signals=%v", result, signaler.calls)
			}
		})
	}
}

func TestSelectedReapRequiresVerifiedExit(t *testing.T) {
	for _, verificationErr := range []error{nil, errors.New("procfs unreadable")} {
		h := defaultHarness()
		p := selectedStray()
		h.discoverSteps = [][]process.Process{{p}, {}}
		signaler := &reapSignaler{}
		selector := dbusReapSelector()
		e := reapEngine(h, signaler, &selector)
		e.SessionExited = func(int, uint64) (bool, error) { return false, verificationErr }
		result := e.ReapProcess(context.Background(), selectedTarget(p))
		if result.OK() || len(signaler.calls) != 1 {
			t.Fatalf("unverified exit result=%+v signals=%v", result, signaler.calls)
		}
	}
}

func TestSelectedReapLocksGuardsAndCancellation(t *testing.T) {
	for _, mode := range []string{"lock", "guard", "cancelled", "discovery failure"} {
		t.Run(mode, func(t *testing.T) {
			h := defaultHarness()
			p := selectedStray()
			h.discoverSteps = [][]process.Process{{p}}
			signaler := &reapSignaler{}
			selector := dbusReapSelector()
			e := reapEngine(h, signaler, &selector)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "lock":
				h.named = []locks.Lock{{Name: "maintenance", State: locks.StateActive}}
			case "guard":
				h.guardBlocked = true
			case "cancelled":
				cancel()
			case "discovery failure":
				h.discoverErrs = []error{errors.New("incomplete process scan")}
			}
			result := e.ReapProcess(ctx, selectedTarget(p))
			if result.OK() || len(signaler.calls) != 0 || len(h.emitted) != 1 {
				t.Fatalf("result=%+v signals=%v events=%v", result, signaler.calls, h.emitted)
			}
		})
	}
}
