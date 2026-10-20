package operation

import (
	"context"
	"slices"
	"syscall"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// Without keep_children every residual must go before a start. A residual the
// kernel places in the unit's own cgroup gets SIGTERM even without force_kill;
// SIGKILL still needs force_kill, and a survivor or a process outside the unit
// keeps the orphan_processes block.
func TestRestartTerminatesUnitCgroupResiduals(t *testing.T) {
	const unitCgroup = "0::/system.slice/mysqld.service"
	member := process.Process{PID: 4242, UID: 500, User: "fran", Exe: "/usr/bin/tmux", ExeOK: true, StartTicks: 9, Cgroup: unitCgroup}
	foreign := member
	foreign.Cgroup = "0::/system.slice/sshd.service"
	for _, tc := range []struct {
		name        string
		residual    process.Process
		survives    bool
		forceKill   bool
		want        ResultStatus
		wantSignals []syscall.Signal
	}{
		{name: "member exits on SIGTERM", residual: member, want: ResultOK, wantSignals: []syscall.Signal{syscall.SIGTERM}},
		{name: "member survives without force_kill", residual: member, survives: true, want: ResultOrphanProcesses, wantSignals: []syscall.Signal{syscall.SIGTERM}},
		{name: "member survives with force_kill", residual: member, survives: true, forceKill: true, want: ResultOrphanProcesses,
			wantSignals: []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}},
		{name: "foreign cgroup is never signalled", residual: foreign, survives: true, want: ResultOrphanProcesses},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			after := []process.Process{}
			if tc.survives {
				after = []process.Process{tc.residual}
			}
			h.discoverSteps = [][]process.Process{{tc.residual}, after}
			h.killPolicy = process.KillPolicy{
				ForceKill: tc.forceKill,
				Member:    func(p process.Process) bool { return servicemgr.CgroupOwnedBy(p.Cgroup, "mysqld") },
			}
			signaler := &pidSignaler{}
			h.reaper = process.Reaper{Signaler: signaler, ResolveUser: rootOnlyResolveUser}

			res := h.engine().Do(context.Background(), actionRestart)
			if res.Status != tc.want {
				t.Fatalf("status = %q (%s), want %q", res.Status, res.Message, tc.want)
			}
			var got []syscall.Signal
			for _, call := range signaler.calls {
				if call.pid != tc.residual.PID {
					t.Fatalf("signalled unexpected pid %d", call.pid)
				}
				got = append(got, call.sig)
			}
			if !slices.Equal(got, tc.wantSignals) {
				t.Fatalf("signals = %v, want %v", got, tc.wantSignals)
			}
			if started := h.mgr.did("start mysqld"); started != (tc.want == ResultOK) {
				t.Fatalf("started = %v, calls = %v", started, h.mgr.calls)
			}
		})
	}
}

// A kept child is delegated by discovery, so a restart neither waits for it nor
// signals it, and still reconciles and starts.
func TestRestartIgnoresKeptChildren(t *testing.T) {
	h := defaultHarness()
	kept := process.Process{PID: 4243, UID: 0, Exe: "/usr/lib64/misc/sshd-session", ExeOK: true, StartTicks: 3,
		Cgroup: "0::/system.slice/mysqld.service", Delegated: true}
	h.discoverSteps = [][]process.Process{{kept}}
	h.killPolicy = process.KillPolicy{ForceKill: true, Member: func(process.Process) bool { return true }}
	signaler := &pidSignaler{}
	h.reaper = process.Reaper{Signaler: signaler, ResolveUser: rootOnlyResolveUser}

	res := h.restart(t)
	if !res.OK() || len(signaler.calls) != 0 {
		t.Fatalf("status = %q (%s), signals = %v", res.Status, res.Message, signaler.calls)
	}
	if want := []string{"stop mysqld", "reset mysqld", "start mysqld"}; !slices.Equal(h.mgr.calls, want) {
		t.Fatalf("calls = %v, want %v", h.mgr.calls, want)
	}
}

// The OpenRC case from fr3: the daemon takes its pidfile along, the backend
// stops listing the unit's control group, and a pre-stop stray survives only as
// a tracked process. Kernel membership of the unit's own group still proves
// ownership, so the stray is signalled without spending the grace period on it;
// one that moved to another group keeps the refusal.
func TestRestartClearsTrackedStrayStillInTheUnitCgroup(t *testing.T) {
	const grace = 30 * time.Second
	for _, tc := range []struct {
		name        string
		cgroup      string
		want        ResultStatus
		wantSignals int
	}{
		{name: "still in the unit group", cgroup: "0::/openrc.mysqld", want: ResultOK, wantSignals: 1},
		{name: "moved to another group", cgroup: "0::/openrc.sshd", want: ResultOrphanProcesses},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := defaultHarness()
			h.backend = string(servicemgr.BackendOpenRC)
			daemon := process.Process{PID: 100, StartTicks: 10, UID: 0, Exe: "/usr/sbin/chronyd", ExeOK: true, Cgroup: "0::/openrc.mysqld", Source: process.SourceBackend}
			stray := process.Process{PID: 200, StartTicks: 20, UID: 0, Exe: "/usr/bin/sleep", ExeOK: true, Cgroup: "0::/openrc.mysqld", Source: process.SourceBackend, Stray: true}
			strayAlive := true
			survivors := func() []process.Process {
				if !strayAlive {
					return nil
				}
				retained := stray
				retained.Stray, retained.Cgroup = false, tc.cgroup
				retained.SignalBlockReason = process.ReasonTrackedOutsideDiscovery
				return []process.Process{retained}
			}
			signaler := &pidSignaler{onSignal: func(pid int, _ syscall.Signal) {
				if pid == stray.PID {
					strayAlive = false
				}
			}}
			h.reaper = process.Reaper{Signaler: signaler, ResolveUser: rootOnlyResolveUser}
			h.killPolicy = process.KillPolicy{
				GracefulTimeout: grace, TermTimeout: 5 * time.Second,
				Member: func(p process.Process) bool { return servicemgr.CgroupOwnedBy(p.Cgroup, "mysqld") },
			}
			e := h.engine()
			e.Lifecycle.ProcessMode = config.ServiceProcessResident
			var slept time.Duration
			e.Sleep = func(d time.Duration) { slept += d }
			observe := func() (process.Observation, error) {
				if !h.mgr.stopped {
					return process.Observation{Processes: []process.Process{daemon, stray}, Trusted: true, IdentityRequired: true}, nil
				}
				return process.Observation{Processes: survivors(), IdentityRequired: true, AbsenceKnown: true}, nil
			}
			e.ObserveProcesses = observe
			e.ObserveTracked = func([]process.Process) (process.Observation, error) { return observe() }
			e.DiscoverTracked = func([]process.Process) ([]process.Process, error) { return survivors(), nil }

			res := e.Restart(t.Context())
			if res.Status != tc.want || len(signaler.calls) != tc.wantSignals {
				t.Fatalf("status = %q (%s) signals = %v, want %q with %d signal(s)", res.Status, res.Message, signaler.calls, tc.want, tc.wantSignals)
			}
			if slept >= grace {
				t.Fatalf("waited %v: a pre-stop stray must not hold the grace period open", slept)
			}
			if started := h.mgr.did("start mysqld"); started != (tc.want == ResultOK) {
				t.Fatalf("started = %v, calls = %v", started, h.mgr.calls)
			}
		})
	}
}
