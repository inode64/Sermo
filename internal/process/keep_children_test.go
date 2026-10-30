package process

import (
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
)

const (
	keepTestDaemon     = "/opt/sermo-test/sshd"
	keepTestSession    = "/opt/sermo-test/sshd-session"
	keepTestSupervisor = "/opt/sermo-test/supervise-daemon"
	keepTestShell      = "/bin/bash"
)

// discoverKept runs Discover over ids with the listener as backend principal and
// returns which PIDs ended delegated.
func discoverKept(t *testing.T, keep bool, ids map[int]Identity, backend []int, selectors []Selector) map[int]bool {
	t.Helper()
	d := Discoverer{
		Reader:       fakeReader{ids: ids},
		ResolveUser:  fakeUsers(map[string]uint32{"root": 0}),
		BackendPIDs:  func() []int { return backend },
		KeepChildren: keep,
	}
	procs, warns := d.Discover(selectors)
	if len(warns) != 0 {
		t.Fatalf("warnings = %v", warns)
	}
	delegated := map[int]bool{}
	for _, proc := range procs {
		delegated[proc.PID] = proc.Delegated
	}
	for pid := range ids {
		if _, ok := delegated[pid]; !ok {
			t.Fatalf("pid %d was not discovered", pid)
		}
	}
	return delegated
}

func TestKeepChildrenDelegatesEverythingButTheTopmostPrincipals(t *testing.T) {
	root := func(pid, ppid int, exe string, cmd ...string) Identity {
		return Identity{PID: pid, PPID: ppid, UID: 0, User: "root", Exe: exe, ExeOK: exe != "", Cmdline: cmd}
	}
	ids := map[int]Identity{
		// OpenRC supervisor above the listener: kept, yet the listener below it
		// stays a principal because no principal is its ancestor.
		5:  root(5, 1, keepTestSupervisor),
		10: root(10, 5, keepTestDaemon, "sshd:", "/usr/sbin/sshd", "[listener]"),
		// OpenSSH >= 9.8 session under a renamed title nothing selects.
		11: root(11, 10, keepTestSession, "sshd-session:", "fran", "[postauth]"),
		12: {PID: 12, PPID: 11, UID: 500, User: "fran", Exe: keepTestShell, ExeOK: true},
		// A same-binary child (OpenSSH < 9.8 session, cronie job runner).
		13: root(13, 10, keepTestDaemon, "sshd:", "fran", "[priv]"),
		// A daemon started from inside a kept session still descends from the listener.
		14: root(14, 12, keepTestDaemon, "/usr/sbin/sshd", "-p", "2222"),
		// A surviving earlier instance reparented to PID 1 stays a principal.
		20: root(20, 1, keepTestDaemon, "sshd:", "/usr/sbin/sshd", "[listener]"),
		// An unreadable executable cannot be told apart from the daemon.
		21: root(21, 1, ""),
	}
	backend := []int{10, 5, 11, 12, 13, 14, 20, 21}
	selectors := []Selector{{Name: RoleMain, Type: SelectorCommandMatch, Exe: keepTestDaemon, User: "root"}}

	got := discoverKept(t, true, ids, backend, selectors)
	for pid, want := range map[int]bool{5: true, 10: false, 11: true, 12: true, 13: true, 14: true, 20: false, 21: false} {
		if got[pid] != want {
			t.Errorf("pid %d delegated = %v, want %v", pid, got[pid], want)
		}
	}

	for pid, delegated := range discoverKept(t, false, ids, backend, selectors) {
		if delegated {
			t.Errorf("without keep_children pid %d must not be delegated", pid)
		}
	}
}

// Kept is what takes a process out of the service's metrics, so it must follow
// keep_children and never a delegated selector, whose target is what the service
// exists to measure.
func TestKeepChildrenMarksOnlyItsOwnDelegationsKept(t *testing.T) {
	ids := map[int]Identity{
		10: {PID: 10, PPID: 1, UID: 0, User: "root", Exe: keepTestDaemon, ExeOK: true},
		11: {PID: 11, PPID: 10, UID: 0, User: "root", Exe: keepTestSession, ExeOK: true},
	}
	backend := []int{10, 11}
	principal := Selector{Name: RoleMain, Type: SelectorCommandMatch, Exe: keepTestDaemon, User: "root"}
	guest := Selector{Name: "guest", Type: SelectorCommandMatch, Exe: keepTestSession, User: "root", Delegated: true}
	discoverer := Discoverer{
		Reader:       fakeReader{ids: ids},
		ResolveUser:  fakeUsers(map[string]uint32{"root": 0}),
		BackendPIDs:  func() []int { return backend },
		KeepChildren: true,
	}

	procs, _ := discoverer.Discover([]Selector{principal, guest})

	got := map[int]Process{}
	for _, p := range procs {
		got[p.PID] = p
	}
	if got[10].Kept {
		t.Error("the principal must stay measured")
	}
	if !got[11].Delegated || got[11].Kept {
		t.Errorf("selector-delegated process = %+v, want delegated and measured", got[11])
	}

	procs, _ = discoverer.Discover([]Selector{principal})
	for _, p := range procs {
		if want := p.PID == 11; p.Kept != want {
			t.Errorf("pid %d kept = %v, want %v", p.PID, p.Kept, want)
		}
	}
}

// A pidfile names the daemon explicitly, so its PID is a principal even under a
// supervisor; the backend's first PID — here that supervisor — is not trusted on
// its own.
func TestKeepChildrenKeepsPidfilePrincipal(t *testing.T) {
	dir := t.TempDir()
	pidfile := dir + "/daemon.pid"
	if err := os.WriteFile(pidfile, []byte("30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ids := map[int]Identity{
		10: {PID: 10, PPID: 1, UID: 0, User: "root", Exe: keepTestSupervisor, ExeOK: true},
		30: {PID: 30, PPID: 10, UID: 0, User: "root", Exe: keepTestDaemon, ExeOK: true},
		31: {PID: 31, PPID: 30, UID: 0, User: "root", Exe: keepTestSession, ExeOK: true},
	}
	got := discoverKept(t, true, ids, []int{10, 30, 31}, []Selector{{Name: "pid", Type: SelectorPidfile, Paths: []string{pidfile}}})
	if got[30] || !got[31] || !got[10] {
		t.Fatalf("delegated = %v, want pidfile 30 the only principal, 10 and 31 kept", got)
	}
}

// Once a systemd unit's daemon exits, the backend lists only what is left in its
// control group, and the first PID is a kept workload process, not the daemon.
// Treating it as a principal would block every restart on the workload the unit
// keeps on purpose (containerd shims, cron jobs, libvirt's dnsmasq).
func TestKeepChildrenNeverPromotesTheBackendsFirstWorkloadPID(t *testing.T) {
	ids := map[int]Identity{
		40: {PID: 40, PPID: 1, UID: 0, User: "root", Exe: "/opt/sermo-test/containerd-shim", ExeOK: true},
		41: {PID: 41, PPID: 1, UID: 0, User: "root", Exe: "/opt/sermo-test/containerd-shim", ExeOK: true},
		42: {PID: 42, PPID: 41, UID: 0, User: "root", Exe: "/opt/sermo-test/nginx", ExeOK: true},
	}
	selectors := []Selector{{Name: RoleMain, Type: SelectorCommandMatch, Exe: "/opt/sermo-test/containerd", User: "root"}}
	for pid, delegated := range discoverKept(t, true, ids, []int{40, 41, 42}, selectors) {
		if !delegated {
			t.Errorf("pid %d must be kept: the daemon is gone and only its workload remains", pid)
		}
	}
}

// A supervisor declared by its own selector stays a principal without hiding
// the daemon it runs: topmost is decided per selector.
func TestKeepChildrenSupervisorSelectorDoesNotHideTheDaemon(t *testing.T) {
	ids := map[int]Identity{
		10: {PID: 10, PPID: 1, UID: 0, User: "root", Exe: keepTestSupervisor, ExeOK: true},
		11: {PID: 11, PPID: 10, UID: 0, User: "root", Exe: keepTestDaemon, ExeOK: true},
		12: {PID: 12, PPID: 11, UID: 0, User: "root", Exe: keepTestSession, ExeOK: true},
	}
	selectors := []Selector{
		{Name: "supervisor", Type: SelectorCommandMatch, Exe: keepTestSupervisor, User: "root"},
		{Name: RoleMain, Type: SelectorCommandMatch, Exe: keepTestDaemon, User: "root"},
	}
	got := discoverKept(t, true, ids, []int{10, 11, 12}, selectors)
	if got[10] || got[11] || !got[12] {
		t.Fatalf("delegated = %v, want supervisor 10 and daemon 11 principals, 12 kept", got)
	}
}

func TestMemberAuthorizesTermAndForceKillOnly(t *testing.T) {
	member := Process{PID: 40, UID: 500, Exe: keepTestShell, ExeOK: true, StartTicks: 1, Cgroup: "0::/openrc.smbd"}
	inUnit := func(p Process) bool { return strings.HasSuffix(p.Cgroup, "openrc.smbd") }
	resolve := fakeUsers(nil)
	forced := KillPolicy{Member: inUnit, ForceKill: true}
	with := func(change func(*Process)) Process {
		p := member
		change(&p)
		return p
	}
	for _, tc := range []struct {
		name     string
		policy   KillPolicy
		proc     Process
		wantTerm bool
		wantKill bool
	}{
		{name: "member without force_kill", policy: KillPolicy{Member: inUnit}, proc: member, wantTerm: true},
		{name: "member with force_kill", policy: forced, proc: member, wantTerm: true, wantKill: true},
		{name: "member with force_kill auto and no strict identity", policy: KillPolicy{Member: inUnit, Automatic: true}, proc: member, wantTerm: true, wantKill: true},
		{name: "keep_children still reaches a principal residual", policy: KillPolicy{Member: inUnit, ForceKill: true, KeepChildren: true}, proc: member, wantTerm: true, wantKill: true},
		{name: "foreign cgroup", policy: forced, proc: with(func(p *Process) { p.Cgroup = "0::/openrc.sshd" })},
		{name: "no cgroup evidence", policy: forced, proc: with(func(p *Process) { p.Cgroup = "" })},
		{name: "delegated", policy: forced, proc: with(func(p *Process) { p.Delegated = true })},
		{name: "unreadable exe", policy: forced, proc: with(func(p *Process) { p.Exe, p.ExeOK = "", false })},
		{name: "unverified deleted exe", policy: forced, proc: with(func(p *Process) { p.Exe, p.ExeOK, p.ExePrev = "", false, keepTestShell })},
		{name: "pid 1", policy: forced, proc: with(func(p *Process) { p.PID = 1 })},
		{name: "no membership probe", policy: KillPolicy{ForceKill: true}, proc: member},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.TermAllowed(tc.proc, resolve); got != tc.wantTerm {
				t.Fatalf("TermAllowed = %v, want %v", got, tc.wantTerm)
			}
			if got := tc.policy.KillAllowed(tc.proc, resolve); got != tc.wantKill {
				t.Fatalf("KillAllowed = %v, want %v", got, tc.wantKill)
			}
			if blocked := tc.policy.BlockReason(tc.proc, resolve) != ""; blocked == tc.wantTerm {
				t.Fatalf("BlockReason blocked = %v, want %v", blocked, !tc.wantTerm)
			}
		})
	}
}

func TestReapMemberTermsWithoutForceKillAndNeverKills(t *testing.T) {
	member := Process{PID: 40, UID: 500, Exe: keepTestShell, ExeOK: true, StartTicks: 1, Cgroup: "0::/openrc.smbd"}
	policy := KillPolicy{Member: func(Process) bool { return true }}
	sig := &recSignaler{}
	res := newReaper(sig, [][]Process{{member}}).Reap(context.Background(), []Process{member}, policy)
	if got := sig.sigsFor(40); len(got) != 1 || got[0] != syscall.SIGTERM {
		t.Fatalf("signals = %v, want SIGTERM only", got)
	}
	if res.OK() || len(res.Remaining) != 1 {
		t.Fatalf("a survivor without force_kill must remain an orphan: %+v", res)
	}

	sig = &recSignaler{}
	policy.ForceKill = true
	res = newReaper(sig, [][]Process{{member}, {}}).Reap(context.Background(), []Process{member}, policy)
	if got := sig.sigsFor(40); len(got) != 2 || got[1] != syscall.SIGKILL || !res.OK() {
		t.Fatalf("signals = %v result = %+v, want TERM then KILL and no residual", got, res)
	}
}

func TestParseStopPolicyKeepChildren(t *testing.T) {
	policy, warnings := ParseStopPolicy(map[string]any{SectionStopPolicy: map[string]any{StopPolicyKeyKeepChildren: true}})
	if !policy.KeepChildren || len(warnings) != 0 || !KeepChildren(map[string]any{SectionStopPolicy: map[string]any{StopPolicyKeyKeepChildren: true}}) {
		t.Fatalf("policy = %+v warnings = %v", policy, warnings)
	}
	policy, warnings = ParseStopPolicy(map[string]any{SectionStopPolicy: map[string]any{StopPolicyKeyKeepChildren: "yes"}})
	if policy.KeepChildren || len(warnings) != 1 {
		t.Fatalf("a non-boolean must warn and keep children off: policy = %+v warnings = %v", policy, warnings)
	}
}
