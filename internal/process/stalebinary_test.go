package process

import "testing"

// staleSelector is the shape a catalog service declares: processes.<name>.exe
// plus a user, the combination that stops matching when a package upgrade
// replaces the binary underneath a running process.
func staleSelector(name, exe, user string) Selector {
	return Selector{Name: name, Type: SelectorCommandMatch, Exe: exe, User: user}
}

// TestDeletedExeNeverMatches pins the safety invariant documented in
// docs/safety.md: a process whose binary was replaced resolves no exe, so it
// matches no selector and can never be selected for signalling. Preserving the
// deleted path for diagnostics must not change this.
func TestDeletedExeNeverMatches(t *testing.T) {
	d := Discoverer{
		Reader: fakeReader{ids: map[int]Identity{
			10: {PID: 10, UID: 0, ExeOK: false, ExePrev: "/usr/bin/ovs-vswitchd"},
		}},
		ResolveUser: fakeUsers(map[string]uint32{"root": 0}),
	}
	sel := staleSelector("main", "/usr/bin/ovs-vswitchd", "root")

	procs, _ := d.Discover([]Selector{sel})
	if len(procs) != 0 {
		t.Fatalf("deleted-exe process must not be discovered, got %v", pidsOf(procs))
	}
	if d.matches(&sel, fakeReaderIdentity(d, 10), d.resolveUser()) {
		t.Fatal("matches() must stay false for a deleted exe")
	}
}

// TestStaleBinariesReportsDiscoveryMiss covers the OpenRC shape seen on
// 192.0.2.131: no backend MainPID, an exe-only selector, and a replaced
// binary. Discovery finds nothing at all, which today reaches the operator as
// an unexplained "no processes" gap.
func TestStaleBinariesReportsDiscoveryMiss(t *testing.T) {
	d := Discoverer{
		Reader: fakeReader{ids: map[int]Identity{
			1928: {PID: 1928, UID: 0, ExeOK: false, ExePrev: "/usr/bin/ovs-vswitchd"},
		}},
		ResolveUser: fakeUsers(map[string]uint32{"root": 0}),
	}

	stale := d.StaleBinaries([]Selector{staleSelector("main", "/usr/bin/ovs-vswitchd", "root")})
	if len(stale) != 1 {
		t.Fatalf("want 1 stale binary, got %d (%v)", len(stale), stale)
	}
	got := stale[0]
	if got.PID != 1928 || got.Path != "/usr/bin/ovs-vswitchd" {
		t.Fatalf("unexpected report: %+v", got)
	}
}

// TestStaleBinariesReportsAttributed covers the systemd shape seen on
// 192.0.2.136: the backend supplies the PID, so the process is attributed
// normally and the replaced binary is today completely silent.
func TestStaleBinariesReportsAttributed(t *testing.T) {
	d := Discoverer{
		Reader: fakeReader{ids: map[int]Identity{
			2915: {PID: 2915, UID: 0, ExeOK: false, ExePrev: "/usr/bin/rpc.mountd"},
		}},
		ResolveUser: fakeUsers(map[string]uint32{"root": 0}),
		BackendPIDs: func() []int { return []int{2915} },
	}

	stale := d.StaleBinaries(nil)
	if len(stale) != 1 {
		t.Fatalf("want 1 stale binary, got %d (%v)", len(stale), stale)
	}
	if stale[0].Path != "/usr/bin/rpc.mountd" {
		t.Fatalf("want the replaced path, got %q", stale[0].Path)
	}
}

// TestStaleBinariesIgnoresHealthyAndForeign keeps the report scoped: a live
// binary is not stale, and a deleted binary belonging to some other program is
// not this service's problem.
func TestStaleBinariesIgnoresHealthyAndForeign(t *testing.T) {
	d := Discoverer{
		Reader: fakeReader{ids: map[int]Identity{
			1: {PID: 1, UID: 0, Exe: "/usr/bin/ovs-vswitchd", ExeOK: true},
			2: {PID: 2, UID: 0, ExeOK: false, ExePrev: "/usr/bin/unrelated"},
		}},
		ResolveUser: fakeUsers(map[string]uint32{"root": 0}),
	}

	if stale := d.StaleBinaries([]Selector{staleSelector("main", "/usr/bin/ovs-vswitchd", "root")}); len(stale) != 0 {
		t.Fatalf("want no stale binaries, got %v", stale)
	}
}

// TestStaleBinariesHonoursUserSelector proves the deleted-exe path reuses the
// same non-exe matching rules, so it cannot report a process owned by someone
// the selector excludes.
func TestStaleBinariesHonoursUserSelector(t *testing.T) {
	d := Discoverer{
		Reader: fakeReader{ids: map[int]Identity{
			7: {PID: 7, UID: 1000, ExeOK: false, ExePrev: "/usr/bin/ovs-vswitchd"},
		}},
		ResolveUser: fakeUsers(map[string]uint32{"root": 0}),
	}

	if stale := d.StaleBinaries([]Selector{staleSelector("main", "/usr/bin/ovs-vswitchd", "root")}); len(stale) != 0 {
		t.Fatalf("selector user must still apply, got %v", stale)
	}
}

func fakeReaderIdentity(d Discoverer, pid int) Identity {
	id, _ := d.reader().Identity(pid)
	return id
}
