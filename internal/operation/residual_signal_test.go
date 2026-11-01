package operation

import (
	"context"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"sermo/internal/locks"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// pidSignaler records the exact (pid, signal) pairs delivered, which the shared
// recordingSignaler flattens into strings and cannot fail on demand.
type pidSignaler struct {
	calls    []pidSignal
	err      error
	onSignal func(int, syscall.Signal)
}

type pidSignal struct {
	pid int
	sig syscall.Signal
}

func (s *pidSignaler) SignalProcess(_ context.Context, target process.Process, sig syscall.Signal) error {
	s.calls = append(s.calls, pidSignal{pid: target.PID, sig: sig})
	if s.err == nil && s.onSignal != nil {
		s.onSignal(target.PID, sig)
	}
	return s.err
}

func strayProc(pid int, exe string) process.Process {
	return process.Process{
		PID: pid, PPID: 1, UID: 0, User: "root",
		Exe: exe, ExeOK: true,
		Role: process.RoleMain, Source: process.SourceBackend, Stray: true,
	}
}

func rootOnlyResolveUser(name string) (uint32, bool) {
	if name == "root" {
		return 0, true
	}
	return 0, false
}

// A stray survives a stop precisely because nothing in the configuration accounts
// for it, so "residual process" alone sends the operator looking for a selector
// that was never written. The stop result must name it and the setting that keeps
// such a workload on purpose.
func TestStopNamesStrayResidualsAndTheSettingThatKeepsThem(t *testing.T) {
	h := defaultHarness()
	survivor := strayProc(4711, "/usr/bin/tmux")
	claimed := process.Process{PID: 4712, User: "root", Exe: "/usr/sbin/sshd", ExeOK: true, Source: process.SourceBackend}
	h.discoverSteps = [][]process.Process{{survivor, claimed}}
	signaler := &pidSignaler{}
	h.reaper = process.Reaper{Signaler: signaler, ResolveUser: rootOnlyResolveUser}

	res := h.action(t, "stop")

	if res.Status != ResultOrphanProcesses {
		t.Fatalf("status = %q (%s), want %q", res.Status, res.Message, ResultOrphanProcesses)
	}
	if len(signaler.calls) != 0 {
		t.Fatalf("a residual without kill authority was signalled: %v", signaler.calls)
	}
	keep := process.StopPolicyFieldPath(process.StopPolicyKeyKeepChildren)
	for _, want := range []string{"2 residual process(es) remain after stop", "1 stray", keep} {
		if !strings.Contains(res.Message, want) {
			t.Fatalf("message %q must contain %q", res.Message, want)
		}
	}
}

// With no stray among them the wording stays exactly as it was: a residual that a
// selector does name is a different problem, with a different fix.
func TestStopResidualsWithoutStraysKeepTheirWording(t *testing.T) {
	h := defaultHarness()
	h.discoverSteps = [][]process.Process{{
		{PID: 4712, User: "root", Exe: "/usr/sbin/sshd", ExeOK: true, Source: process.SourceBackend},
	}}
	signaler := &pidSignaler{}
	h.reaper = process.Reaper{Signaler: signaler, ResolveUser: rootOnlyResolveUser}

	res := h.action(t, "stop")

	if want := "1 residual process(es) remain after stop"; res.Message != want {
		t.Fatalf("message = %q, want exactly %q", res.Message, want)
	}
	if len(signaler.calls) != 0 {
		t.Fatalf("a residual without kill authority was signalled: %v", signaler.calls)
	}
}

// A residual Sermo may terminate but cannot signal (the kernel refuses with
// EPERM) stays an orphan: the stop reports the refusal and the restart never
// starts on top of it.
func TestRestartBlocksWhenResidualSignalIsRefused(t *testing.T) {
	h := defaultHarness()
	residual := process.Process{PID: 4242, StartTicks: 9, UID: 0, User: "root", Exe: "/usr/bin/tmux", ExeOK: true, Cgroup: "0::/system.slice/mysqld.service"}
	h.discoverSteps = [][]process.Process{{residual}}
	h.killPolicy = process.KillPolicy{Member: func(process.Process) bool { return true }}
	signaler := &pidSignaler{err: syscall.EPERM}
	h.reaper = process.Reaper{Signaler: signaler, ResolveUser: rootOnlyResolveUser}

	res := h.restart(t)

	if res.Status != ResultOrphanProcesses {
		t.Fatalf("status = %q (%s), want %q", res.Status, res.Message, ResultOrphanProcesses)
	}
	if want := "residual pid 4242: deliver signal: operation not permitted"; !strings.Contains(res.Message, want) {
		t.Fatalf("message %q must report %q", res.Message, want)
	}
	if len(signaler.calls) == 0 || signaler.calls[0] != (pidSignal{pid: 4242, sig: syscall.SIGTERM}) {
		t.Fatalf("signals = %v, want a SIGTERM attempt on 4242", signaler.calls)
	}
	if h.mgr.did("start mysqld") {
		t.Fatalf("restart started after a refused residual signal: %v", h.mgr.calls)
	}
}

// A selector's exact path and UID also match inside a container, whose
// /proc/<pid>/exe reads as the guest sees it. A container's control group is
// never a host service's, so its process is neither attributed nor signalled,
// not even by the membership authority that needs no force_kill; the unit's own
// survivor is still cleared.
func TestStartLeavesContainerProcessesAlone(t *testing.T) {
	const exe = "/opt/sermo-test/nginx"
	for _, tc := range []struct {
		name        string
		cgroup      string
		forceKill   any
		wantSignals []pidSignal
	}{
		{name: "cgroupfs container under auto", cgroup: "0::/docker/0123abcd", forceKill: process.StopPolicyForceKillAuto},
		{name: "kubernetes pod under auto", cgroup: "0::/kubepods/burstable/pod1/ctr1", forceKill: process.StopPolicyForceKillAuto},
		{name: "systemd guest of the same unit name", cgroup: "0::/lxc.payload.web/system.slice/nginx.service", forceKill: false},
		{name: "openrc guest of the same unit name", cgroup: "0::/lxc.payload.web/openrc.nginx", forceKill: false},
		{name: "the unit's own survivor", cgroup: "0::/openrc.nginx", forceKill: false, wantSignals: []pidSignal{{pid: 500, sig: syscall.SIGTERM}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &countingPIDReader{ids: map[int]process.Identity{
				500: {PID: 500, PPID: 480, UID: 0, StartTicks: 5, StartTicksOK: true, Exe: exe, ExeOK: true, Cgroup: tc.cgroup, State: "S"},
			}}
			dir := t.TempDir()
			locker := locks.NewOperationLocker(locks.RuntimeOpsDir(dir))
			engine := New(Config{
				Service: "nginx",
				Unit:    "nginx",
				Backend: string(servicemgr.BackendOpenRC),
				Tree: map[string]any{
					process.SectionStopPolicy: map[string]any{
						process.StopPolicyKeyForceKill:   tc.forceKill,
						process.StopPolicyKeyTermTimeout: "1s",
						process.StopPolicyKeyKillTimeout: "1s",
					},
					process.SectionProcesses: map[string]any{
						"main": map[string]any{process.SelectorKeyExe: exe, process.SelectorKeyUser: "root"},
					},
				},
				Manager:    &fakeManager{status: servicemgr.StatusInactive},
				Locker:     &locker,
				Scanner:    locks.NewScanner(locks.RuntimeLocksDir(dir)),
				Discoverer: process.Discoverer{Reader: reader, ResolveUser: rootOnlyResolveUser},
				Sleep:      func(time.Duration) {},
			})
			signaler := &pidSignaler{onSignal: func(pid int, _ syscall.Signal) { delete(reader.ids, pid) }}
			engine.Reaper.Signaler = signaler

			engine.Do(context.Background(), actionStart)

			if !slices.Equal(signaler.calls, tc.wantSignals) {
				t.Fatalf("signals = %v, want %v", signaler.calls, tc.wantSignals)
			}
		})
	}
}
