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
// the domain does and exits exitAfter into the stop.
type guestTable struct {
	mgr       *fakeManager
	emulator  process.Identity
	missing   bool
	exitAfter time.Duration
	slept     time.Duration
}

func (r *guestTable) current() map[int]process.Identity {
	if r.missing || (r.mgr.stopped && r.slept >= r.exitAfter) {
		return nil
	}
	return map[int]process.Identity{r.emulator.PID: r.emulator}
}

func (r *guestTable) PIDs() ([]int, error) {
	pids := make([]int, 0, 1)
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
		exitAfter time.Duration
	}{
		{name: "gone with the stop"},
		{name: "gone during the wait", exitAfter: 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			table := &guestTable{mgr: &fakeManager{status: servicemgr.StatusActive}, exitAfter: tc.exitAfter}
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
