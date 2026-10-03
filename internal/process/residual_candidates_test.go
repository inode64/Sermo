package process

import (
	"testing"

	"sermo/internal/servicemgr"
)

func TestObserveDeletedHelperBeforeAndAfterStop(t *testing.T) {
	t.Parallel()
	const helper = "/opt/squid/pinger"
	ids := map[int]Identity{
		100: {PID: 100, PPID: 1, UID: 110, Exe: testExe, ExeOK: true, Cgroup: "0::/system.slice/mysql.service"},
		200: {PID: 200, PPID: 1, UID: 110, ExePrev: helper, ExeFile: ExecutableFile{Device: 1, Inode: 20}, Cgroup: "0::/user.slice/session-1.scope"},
	}
	backend := []int{100}
	d := Discoverer{Reader: fakeReader{ids: ids}, ResolveUser: fakeUsers(map[string]uint32{"mysql": 110}), BackendPIDs: func() []int { return backend },
		ProcessOwnership: func(id Identity) (bool, bool) { return servicemgr.CgroupOwnership(id.Cgroup, "mysql") }}
	selectors := []Selector{staleSelector(RoleMain, testExe, "mysql"), staleSelector("pinger", helper, "mysql")}
	policy := EnableAutomaticReaping(KillPolicy{Automatic: true}, selectors)
	for _, phase := range []string{"before", "after"} {
		if phase == "after" {
			delete(ids, 100)
			backend = nil
		}
		out, err := d.Observe(selectors)
		if err != nil {
			t.Fatal(err)
		}
		wantCount := 2
		if phase == "after" {
			wantCount = 1
		}
		if len(out.Processes) != wantCount || out.Trusted != (phase == "before") {
			t.Fatalf("%s: %+v", phase, out)
		}
		p := out.Processes[len(out.Processes)-1]
		if p.PID != 200 || p.Role != "pinger" || !p.External || !policy.KillOnlyIf.Killable(p, d.resolveUser()) {
			t.Fatalf("%s helper not authorized: %+v", phase, p)
		}
	}
}

func TestDeletedCandidateOwnership(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		group     string
		mainGroup string
		otherMain bool
		wantCount int
		wantBlock bool
	}{
		{name: "old session", mainGroup: "0::/system.slice/mysql.service", group: "0::/user.slice/session-1.scope", wantCount: 2},
		{name: "other unit", group: "0::/system.slice/other.service", wantCount: 1},
		{name: "unknown cgroup", wantCount: 2, wantBlock: true},
		{name: "unknown main cgroup", group: "0::/user.slice/session-1.scope", wantCount: 2, wantBlock: true},
		{name: "malformed main cgroup", mainGroup: "bad", group: "0::/user.slice/session-1.scope", wantCount: 2, wantBlock: true},
		{name: "foreign main cgroup", mainGroup: "0::/system.slice/other.service", group: "0::/user.slice/session-1.scope", wantCount: 2, wantBlock: true},
		{name: "shared helper multiple instances", mainGroup: "0::/system.slice/mysql.service", group: "0::/user.slice/session-1.scope", otherMain: true, wantCount: 2, wantBlock: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ids := map[int]Identity{
				100: {PID: 100, PPID: 1, UID: 110, Exe: testExe, ExeOK: true, Cgroup: tc.mainGroup},
				200: {PID: 200, PPID: 1, UID: 110, ExePrev: "/opt/helper", ExeFile: ExecutableFile{Inode: 20}, Cgroup: tc.group},
			}
			if tc.otherMain {
				ids[300] = Identity{PID: 300, PPID: 1, UID: 110, Exe: testExe, ExeOK: true}
			}
			d := Discoverer{Reader: fakeReader{ids: ids}, ResolveUser: fakeUsers(map[string]uint32{"mysql": 110}), BackendPIDs: func() []int { return []int{100} },
				ProcessOwnership: func(id Identity) (bool, bool) { return servicemgr.CgroupOwnership(id.Cgroup, "mysql") }}
			out, err := d.Observe([]Selector{staleSelector(RoleMain, testExe, "mysql"), staleSelector("helper", "/opt/helper", "mysql")})
			if err != nil || len(out.Processes) != tc.wantCount {
				t.Fatalf("%+v, %v", out, err)
			}
			if tc.wantCount > 1 && (out.Processes[1].SignalBlockReason != "") != tc.wantBlock {
				t.Fatalf("candidate=%+v", out.Processes[1])
			}
			if tc.wantBlock && killPolicy.KillOnlyIf.Killable(out.Processes[1], d.resolveUser()) {
				t.Fatal("uncertain candidate is signalable")
			}
		})
	}
}

func TestExternalDeletedCandidateRemainsDelegated(t *testing.T) {
	t.Parallel()
	p := deletedSignalTarget()
	d := Discoverer{Reader: fakeReader{ids: map[int]Identity{
		p.PID: {PID: p.PID, UID: p.UID, ExePrev: p.ExePrev, ExeFile: p.ExeFile},
	}}, ResolveUser: fakeUsers(map[string]uint32{"mysql": 110}),
		ProcessOwnership: func(Identity) (bool, bool) { return false, true }}
	main := staleSelector(RoleMain, testExe, "mysql")
	delegated := main
	delegated.Delegated = true
	out, err := d.Observe([]Selector{main, delegated})
	if err != nil || len(out.Processes) != 1 || !out.Processes[0].Delegated {
		t.Fatalf("delegation lost: %+v err=%v", out, err)
	}
	if killPolicy.KillOnlyIf.Killable(out.Processes[0], d.resolveUser()) {
		t.Fatal("delegated candidate authorized for cleanup")
	}
}
