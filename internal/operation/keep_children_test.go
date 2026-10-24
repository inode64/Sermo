package operation

import (
	"context"
	"maps"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/locks"
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
				Member:    UnitCgroupMember("mysqld"),
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
				Member: UnitCgroupMember("mysqld"),
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

			res := e.Do(t.Context(), actionRestart)
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

// keepChildrenTable is the process table of a daemon whose session child
// outlives it in the unit's cgroup: the daemon goes with the stop (the child
// reparents to PID 1), a replacement appears with the start, and the child exits
// only when it is signalled.
type keepChildrenTable struct {
	mgr                          *fakeManager
	daemon, session, replacement process.Identity
	sessionGone                  bool
}

func (r *keepChildrenTable) current() map[int]process.Identity {
	ids := map[int]process.Identity{}
	started := slices.ContainsFunc(r.mgr.calls, func(call string) bool { return strings.HasPrefix(call, "start ") })
	switch {
	case started:
		ids[r.replacement.PID] = r.replacement
	case !r.mgr.stopped:
		ids[r.daemon.PID] = r.daemon
	}
	if !r.sessionGone {
		session := r.session
		if r.mgr.stopped || started {
			session.PPID = 1
		}
		ids[session.PID] = session
	}
	return ids
}

func (r *keepChildrenTable) PIDs() ([]int, error) { return slices.Collect(maps.Keys(r.current())), nil }

func (r *keepChildrenTable) Identity(pid int) (process.Identity, bool) {
	id, ok := r.current()[pid]
	return id, ok
}

// stop_policy.keep_children reaches discovery through New: with it the session
// child that outlives its daemon in the unit's cgroup is kept and never
// signalled; without it the same child is a residual of the unit's own cgroup,
// terminated before the start.
func TestNewKeepChildrenReachesDiscovery(t *testing.T) {
	const cgroup = "0::/system.slice/sshd.service"
	daemon := process.Identity{PID: 100, PPID: 1, UID: 0, StartTicks: 10, StartTicksOK: true, Exe: "/opt/sermo-test/sshd", ExeOK: true, Cgroup: cgroup, State: "S"}
	session := process.Identity{PID: 200, PPID: 100, UID: 0, StartTicks: 20, StartTicksOK: true, Exe: "/opt/sermo-test/sshd-session", ExeOK: true, Cgroup: cgroup, State: "S"}
	replacement := daemon
	replacement.PID, replacement.StartTicks = 300, 30
	for _, tc := range []struct {
		name        string
		keep        bool
		wantSignals []pidSignal
	}{
		{name: "keep_children keeps the session", keep: true},
		{name: "default terminates the unit-cgroup residual", wantSignals: []pidSignal{{pid: 200, sig: syscall.SIGTERM}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			locker := locks.NewOperationLocker(locks.RuntimeOpsDir(dir))
			mgr := &fakeManager{status: servicemgr.StatusActive}
			table := &keepChildrenTable{mgr: mgr, daemon: daemon, session: session, replacement: replacement}
			engine := New(Config{
				Service: "sshd",
				Unit:    "sshd",
				Backend: string(servicemgr.BackendSystemd),
				Tree: map[string]any{
					process.SectionStopPolicy: map[string]any{process.StopPolicyKeyKeepChildren: tc.keep},
					process.SectionProcesses: map[string]any{
						"main": map[string]any{process.SelectorKeyExe: "/opt/sermo-test/sshd", process.SelectorKeyUser: "root"},
					},
				},
				Manager: mgr,
				Locker:  &locker,
				Scanner: locks.NewScanner(locks.RuntimeLocksDir(dir)),
				Discoverer: process.Discoverer{
					Reader:      table,
					ResolveUser: rootOnlyResolveUser,
					BackendPIDs: func() []int { return slices.Sorted(maps.Keys(table.current())) },
				},
				Sleep: func(time.Duration) {},
			})
			signaler := &pidSignaler{onSignal: func(pid int, _ syscall.Signal) {
				if pid == session.PID {
					table.sessionGone = true
				}
			}}
			engine.Reaper.Signaler = signaler

			res := engine.Do(context.Background(), actionRestart)
			if res.Status != ResultOK {
				t.Fatalf("status = %q (%s), want ok", res.Status, res.Message)
			}
			if !slices.Equal(signaler.calls, tc.wantSignals) {
				t.Fatalf("signals = %v, want %v", signaler.calls, tc.wantSignals)
			}
			if table.sessionGone == tc.keep {
				t.Fatalf("session gone = %v with keep_children %v", table.sessionGone, tc.keep)
			}
		})
	}
}
