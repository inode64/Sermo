package process

import (
	"slices"
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

// The global selector fallback never attributes a process another unit owns,
// even when its exe and user match the service's selector exactly: a stopped
// mysql must not claim a mysqld that runs in a different unit.
func TestDiscoverFallbackSkipsForeignUnitProcess(t *testing.T) {
	t.Parallel()
	ids := map[int]Identity{
		100: {PID: 100, PPID: 1, StartTicks: 1, UID: 110, Exe: testExe, ExeOK: true, Cgroup: "0::/system.slice/mysql.service"},
		200: {PID: 200, PPID: 1, StartTicks: 2, UID: 110, Exe: testExe, ExeOK: true, Cgroup: "0::/system.slice/mysql-replica.service"},
	}
	d := Discoverer{
		Reader:           fakeReader{ids},
		ResolveUser:      fakeUsers(map[string]uint32{"mysql": 110}),
		ProcessOwnership: func(id Identity) (bool, bool) { return servicemgr.CgroupOwnership(id.Cgroup, "mysql") },
	}
	found, _ := d.Discover([]Selector{{Name: "main", Type: SelectorCommandMatch, Exe: testExe, User: "mysql"}})
	if len(found) != 1 || found[0].PID != 100 {
		t.Fatalf("found = %+v, want only the mysql unit's pid 100", found)
	}
}

// A delegated selector names a process another manager owns, so its usual home
// is another unit's group: libvirt keeps a NAT network's dnsmasq pair in its
// daemon's service and a guest's emulator in a machine scope. Those matches are
// observe-only and must still be attributed; a signallable selector stays
// scoped to the service's own unit.
func TestDiscoverDelegatedSelectorAttributesProcessOwnedElsewhere(t *testing.T) {
	t.Parallel()
	const (
		dnsmasq   = "/usr/bin/dnsmasq"
		qemu      = "/usr/bin/qemu-system-x86_64"
		dnsmasqRe = `--conf-file=/var/lib/libvirt/dnsmasq/default\.conf([[:space:]]|$)`
	)
	dnsmasqPair := map[int]Identity{
		100: {PID: 100, PPID: 1, UID: 65534, Exe: dnsmasq, ExeOK: true, Cgroup: "0::/system.slice/libvirtd.service",
			Cmdline: []string{dnsmasq, "--conf-file=/var/lib/libvirt/dnsmasq/default.conf"}},
		101: {PID: 101, PPID: 1, UID: 0, Exe: dnsmasq, ExeOK: true, Cgroup: "0::/system.slice/libvirtd.service",
			Cmdline: []string{dnsmasq, "--conf-file=/var/lib/libvirt/dnsmasq/default.conf"}},
	}
	for _, tc := range []struct {
		name      string
		unit      string
		ids       map[int]Identity
		selectors []Selector
		want      []int
	}{
		{
			name: "network dnsmasq in the daemon's group",
			unit: "default",
			ids:  dnsmasqPair,
			selectors: []Selector{
				{Name: "dnsmasq-root", Type: SelectorCommandMatch, Exe: dnsmasq, User: "root", Cmd: dnsmasqRe, Delegated: true},
				{Name: "dnsmasq-nobody", Type: SelectorCommandMatch, Exe: dnsmasq, User: "nobody", Cmd: dnsmasqRe, Delegated: true},
			},
			want: []int{100, 101},
		},
		{
			name: "guest emulator in its machine scope",
			unit: "web01",
			ids: map[int]Identity{
				200: {PID: 200, PPID: 1, UID: 77, Exe: qemu, ExeOK: true, Cgroup: `0::/machine.slice/machine-qemu\x2d1\x2dweb01.scope/libvirt/emulator`,
					Cmdline: []string{qemu, "-name", "guest=web01,debug-threads=on"}},
			},
			selectors: []Selector{{Name: "main", Type: SelectorCommandMatch, Cmd: `^\S*qemu-system\S* -name guest=web01,.*$`, Delegated: true}},
			want:      []int{200},
		},
		{
			name:      "non-delegated selector stays scoped",
			unit:      "default",
			ids:       dnsmasqPair,
			selectors: []Selector{{Name: "dnsmasq-nobody", Type: SelectorCommandMatch, Exe: dnsmasq, User: "nobody", Cmd: dnsmasqRe}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := Discoverer{
				Reader:           fakeReader{tc.ids},
				ResolveUser:      fakeUsers(map[string]uint32{"root": 0, "nobody": 65534}),
				ProcessOwnership: func(id Identity) (bool, bool) { return servicemgr.CgroupOwnership(id.Cgroup, tc.unit) },
			}
			found, _ := d.Discover(tc.selectors)
			if got := pidsOf(found); !slices.Equal(got, tc.want) {
				t.Fatalf("pids = %v, want %v", got, tc.want)
			}
			for _, proc := range found {
				if !proc.Delegated {
					t.Fatalf("pid %d must stay observe-only: %+v", proc.PID, proc)
				}
			}
		})
	}
}
