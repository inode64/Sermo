package operation

import (
	"context"
	"testing"
	"time"

	"sermo/internal/locks"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

const observeOnlyEmulatorCmd = `^\S*qemu-system\S* -name guest=web01,.*$`

// guestTable is the process table of a libvirt domain: its emulator runs while
// the domain does and exits exitAfter into the stop. An optional vhost-user
// backend sharing the guest's memory exits backendExitAfter into the stop.
type guestTable struct {
	mgr              *fakeManager
	emulator         process.Identity
	backend          *process.Identity
	missing          bool
	exitAfter        time.Duration
	backendExitAfter time.Duration
	slept            time.Duration
}

func (r *guestTable) current() map[int]process.Identity {
	if r.missing {
		return nil
	}
	table := map[int]process.Identity{}
	if !r.mgr.stopped || r.slept < r.exitAfter {
		table[r.emulator.PID] = r.emulator
	}
	if r.backend != nil && (!r.mgr.stopped || r.slept < r.backendExitAfter) {
		table[r.backend.PID] = *r.backend
	}
	return table
}

func (r *guestTable) PIDs() ([]int, error) {
	pids := make([]int, 0, 2)
	for pid := range r.current() {
		pids = append(pids, pid)
	}
	return pids, nil
}

func (r *guestTable) Identity(pid int) (process.Identity, bool) {
	id, ok := r.current()[pid]
	return id, ok
}

func newObserveOnlyEngine(t *testing.T, table *guestTable, processes, stopPolicy map[string]any) Engine {
	t.Helper()
	dir := t.TempDir()
	locker := locks.NewOperationLocker(locks.RuntimeOpsDir(dir))
	table.emulator = process.Identity{
		PID: 900, PPID: 1, UID: 77, StartTicks: 9, StartTicksOK: true, Exe: "/opt/sermo-test/qemu-system-x86_64", ExeOK: true, State: "S",
		Cmdline: []string{"/opt/sermo-test/qemu-system-x86_64", "-name", "guest=web01,debug-threads=on"},
		Cgroup:  `0::/machine.slice/machine-qemu\x2d1\x2dweb01.scope/libvirt/emulator`,
	}
	engine := New(Config{
		Service: "vm-web01",
		Unit:    "web01",
		Backend: string(servicemgr.BackendLibvirt),
		Tree: map[string]any{
			process.SectionProcesses:  processes,
			process.SectionStopPolicy: stopPolicy,
		},
		Manager:    table.mgr,
		Locker:     &locker,
		Scanner:    locks.NewScanner(locks.RuntimeLocksDir(dir)),
		Discoverer: process.Discoverer{Reader: table, ResolveUser: rootOnlyResolveUser},
		Sleep:      func(d time.Duration) { table.slept += d },
	})
	engine.ResumeFunc = func(ctx context.Context) error { return table.mgr.Resume(ctx, "web01") }
	return engine
}

func observeOnlySelector() map[string]any {
	return map[string]any{
		"main": map[string]any{process.SelectorKeyCmd: observeOnlyEmulatorCmd, process.SelectorKeyDelegated: true},
	}
}

// A service whose every selector is delegated has no process of its own: the
// emulator it observes is the only proof that the backend's active state is real.
// That proof stays required, and it never stands in for a principal a mixed
// service declares.
func TestActionsOnObserveOnlyService(t *testing.T) {
	mixed := observeOnlySelector()
	mixed["daemon"] = map[string]any{process.SelectorKeyExe: "/opt/sermo-test/daemon", process.SelectorKeyUser: "root"}
	for _, tc := range []struct {
		name      string
		action    string
		processes map[string]any
		stopped   bool
		missing   bool
		want      ResultStatus
	}{
		{name: "start", action: actionStart, processes: observeOnlySelector(), stopped: true, want: ResultOK},
		{name: "restart", action: actionRestart, processes: observeOnlySelector(), want: ResultOK},
		{name: "restart of a shut off guest", action: actionRestart, processes: observeOnlySelector(), stopped: true, want: ResultOK},
		{name: "resume", action: actionResume, processes: observeOnlySelector(), want: ResultOK},
		{name: "start with no emulator", action: actionStart, processes: observeOnlySelector(), stopped: true, missing: true, want: ResultFailed},
		{name: "a declared principal stays required", action: actionStart, processes: mixed, stopped: true, want: ResultFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := &guestTable{mgr: &fakeManager{status: servicemgr.StatusActive, stopped: tc.stopped}, missing: tc.missing}
			engine := newObserveOnlyEngine(t, table, tc.processes, nil)

			res := engine.Do(context.Background(), tc.action)

			if res.Status != tc.want {
				t.Fatalf("status = %q (%s), want %q; calls = %v", res.Status, res.Message, tc.want, table.mgr.calls)
			}
		})
	}
}

// With no selector of its own to prove absence, an observe-only service ends its
// stop wait on the generations it saw, like any service without a strict
// selector, instead of sitting out the whole grace period.
func TestStopOfObserveOnlyServiceEndsWithItsGeneration(t *testing.T) {
	const grace = 30 * time.Second
	for _, tc := range []struct {
		name      string
		status    servicemgr.Status
		exitAfter time.Duration
	}{
		{name: "gone with the stop", status: servicemgr.StatusActive},
		{name: "gone during the wait", status: servicemgr.StatusActive, exitAfter: 5 * time.Second},
		// The backend powers off a paused guest QEMU can no longer run.
		{name: "paused guest gone with the stop", status: servicemgr.StatusPaused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := &guestTable{mgr: &fakeManager{status: tc.status}, exitAfter: tc.exitAfter}
			engine := newObserveOnlyEngine(t, table, observeOnlySelector(), map[string]any{process.StopPolicyKeyGracefulTimeout: grace.String()})

			res := engine.Do(context.Background(), actionStop)

			if res.Status != ResultOK {
				t.Fatalf("status = %q (%s)", res.Status, res.Message)
			}
			if table.slept < tc.exitAfter || table.slept >= grace {
				t.Fatalf("waited %v, want the %v the emulator took", table.slept, tc.exitAfter)
			}
		})
	}
}

// A guest's vhost-user backends (virtiofsd) map its memory and can outlive
// QEMU by a moment after a stop. Attributed with their own delegated selector
// they are generations the stop waits for, so a restart cannot start the next
// guest while one still holds the old RAM; unattributed, the wait ends with
// QEMU.
func TestStopOfObserveOnlyServiceWaitsForItsBackends(t *testing.T) {
	const (
		grace       = 30 * time.Second
		backendExit = 2 * time.Second
		backendCmd  = `(^|\s)--shared-dir[= ](/srv/virtiofs/web01/data)(\s|$)`
	)
	withBackend := observeOnlySelector()
	withBackend["virtiofsd"] = map[string]any{
		process.SelectorKeyExe: "/opt/sermo-test/virtiofsd", process.SelectorKeyUser: "root",
		process.SelectorKeyCmd: backendCmd, process.SelectorKeyDelegated: true,
	}
	for _, tc := range []struct {
		name      string
		processes map[string]any
		waited    func(time.Duration) bool
	}{
		{name: "attributed backend", processes: withBackend, waited: func(d time.Duration) bool { return d >= backendExit && d < grace }},
		{name: "unattributed backend", processes: observeOnlySelector(), waited: func(d time.Duration) bool { return d < backendExit }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := &guestTable{mgr: &fakeManager{status: servicemgr.StatusActive}, backendExitAfter: backendExit}
			engine := newObserveOnlyEngine(t, table, tc.processes, map[string]any{process.StopPolicyKeyGracefulTimeout: grace.String()})
			table.backend = &process.Identity{
				PID: 901, PPID: 1, UID: 0, StartTicks: 11, StartTicksOK: true, Exe: "/opt/sermo-test/virtiofsd", ExeOK: true, State: "S",
				Cmdline: []string{"/opt/sermo-test/virtiofsd", "--fd=3", "--shared-dir=/srv/virtiofs/web01/data"},
				Cgroup:  `0::/system.slice/system-virtiofsd.slice/virtiofsd@web01_data.service`,
			}

			res := engine.Do(context.Background(), actionStop)

			if res.Status != ResultOK {
				t.Fatalf("status = %q (%s)", res.Status, res.Message)
			}
			if !tc.waited(table.slept) {
				t.Fatalf("waited %v; backend exits %v into the stop", table.slept, backendExit)
			}
		})
	}
}
