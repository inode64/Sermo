package process

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// strayCgroup models a systemd unit whose control group holds the daemon (100),
// a worker it spawned (200) and two processes reparented to PID 1 that no
// selector claims (300, 400) — the shape of the leak that exhausted bk1.
func strayCgroup() fakeReader {
	return fakeReader{ids: map[int]Identity{
		1:   {PID: 1, PPID: 0, Exe: "/sbin/init", ExeOK: true},
		100: {PID: 100, PPID: 1, UID: 0, Exe: "/usr/bin/daemon", ExeOK: true},
		200: {PID: 200, PPID: 100, UID: 0, Exe: "/usr/bin/daemon", ExeOK: true},
		300: {PID: 300, PPID: 1, UID: 0, Exe: "/usr/bin/dbus-daemon", ExeOK: true},
		400: {PID: 400, PPID: 1, UID: 0, Exe: "/usr/bin/dbus-daemon", ExeOK: true},
	}}
}

func strayDiscoverer(reader Reader, cgroup ...int) Discoverer {
	return Discoverer{
		Reader:      reader,
		ResolveUser: fakeUsers(map[string]uint32{"root": 0, "nobody": 65534}),
		BackendPIDs: func() []int { return cgroup },
	}
}

func strayPIDs(procs []Process) []int {
	var out []int
	for _, p := range procs {
		if p.Stray {
			out = append(out, p.PID)
		}
	}
	sort.Ints(out)
	return out
}

func assertPIDs(t *testing.T, label string, got, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

func TestDiscoverMarksReparentedCgroupMembersStray(t *testing.T) {
	d := strayDiscoverer(strayCgroup(), 100, 200, 300, 400)
	procs, _ := d.Discover(nil)

	assertPIDs(t, "strays", strayPIDs(procs), []int{300, 400})
	for _, p := range procs {
		if p.PID == 100 && p.Stray {
			t.Fatal("the principal process must never be a stray")
		}
		if p.PID == 200 && p.Stray {
			t.Fatal("a descendant of the principal process must never be a stray")
		}
	}
}

func TestDiscoverStrayExcludesSelectorMatches(t *testing.T) {
	d := strayDiscoverer(strayCgroup(), 100, 200, 300, 400)
	selectors := []Selector{{
		Name: "bus",
		Type: SelectorCommandMatch,
		Exe:  "/usr/bin/dbus-daemon",
		User: "root",
	}}
	procs, _ := d.Discover(selectors)

	if got := strayPIDs(procs); len(got) != 0 {
		t.Fatalf("a claimed process must not be a stray: %v", got)
	}
}

// A selector that matches on identity but not on user leaves the process
// unclaimed: the selector describes a different role, so Sermo still cannot say
// what this process is.
func TestDiscoverStrayKeepsProcessesASelectorNarrowlyMisses(t *testing.T) {
	d := strayDiscoverer(strayCgroup(), 100, 200, 300, 400)
	selectors := []Selector{{
		Name: "bus",
		Type: SelectorCommandMatch,
		Exe:  "/usr/bin/dbus-daemon",
		User: "nobody",
	}}
	procs, _ := d.Discover(selectors)

	assertPIDs(t, "strays", strayPIDs(procs), []int{300, 400})
}

func TestDiscoverStrayExcludesDelegated(t *testing.T) {
	d := strayDiscoverer(strayCgroup(), 100, 200, 300, 400)
	selectors := []Selector{{
		Name:      "brick",
		Type:      SelectorCommandMatch,
		Exe:       "/usr/bin/dbus-daemon",
		User:      "root",
		Delegated: true,
	}}
	procs, _ := d.Discover(selectors)

	for _, p := range procs {
		if p.Stray {
			t.Fatalf("pid %d is delegated and must not be a stray", p.PID)
		}
		if (p.PID == 300 || p.PID == 400) && !p.Delegated {
			t.Fatalf("pid %d should have stayed delegated", p.PID)
		}
	}
}

// A process whose binary was replaced on disk is the one its selector was written
// for; it just cannot prove it any more. The stale-binary report already names it,
// so labelling it stray on top would be a second, contradictory answer.
func TestDiscoverStrayExcludesReplacedBinaryMatches(t *testing.T) {
	reader := fakeReader{ids: map[int]Identity{
		100: {PID: 100, PPID: 1, UID: 0, Exe: "/usr/bin/daemon", ExeOK: true},
		300: {PID: 300, PPID: 1, UID: 0, ExePrev: "/usr/bin/worker"},
	}}
	d := strayDiscoverer(reader, 100, 300)
	selectors := []Selector{{Name: "worker", Type: SelectorCommandMatch, Exe: "/usr/bin/worker", User: "root"}}
	procs, _ := d.Discover(selectors)

	if got := strayPIDs(procs); len(got) != 0 {
		t.Fatalf("a process a selector would have matched must not be a stray: %v", got)
	}
}

// Without a `processes:` block the daemon's own workers are still its
// descendants, so a service that declares no selector at all must not have its
// whole control group reported as unaccounted for.
func TestDiscoverStrayWithoutSelectorsSparesTheDaemonTree(t *testing.T) {
	reader := fakeReader{ids: map[int]Identity{
		100: {PID: 100, PPID: 1, UID: 0, Exe: "/usr/sbin/nginx", ExeOK: true},
		200: {PID: 200, PPID: 100, UID: 33, Exe: "/usr/sbin/nginx", ExeOK: true},
		201: {PID: 201, PPID: 100, UID: 33, Exe: "/usr/sbin/nginx", ExeOK: true},
	}}
	d := strayDiscoverer(reader, 100, 200, 201)
	procs, _ := d.Discover(nil)

	if got := strayPIDs(procs); len(got) != 0 {
		t.Fatalf("workers of the principal process must not be strays: %v", got)
	}
}

// A daemon named by its own pidfile keeps the backend seed's source (step 0 wins
// the first write), so without the pidfile record it would look unclaimed.
func TestDiscoverStrayExcludesPidfilePIDs(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "daemon.pid")
	if err := os.WriteFile(pidfile, []byte("300\n"), 0o600); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	d := strayDiscoverer(strayCgroup(), 100, 300)
	selectors := []Selector{{Name: SelectorPidfile, Type: SelectorPidfile, Paths: []string{pidfile}}}
	procs, _ := d.Discover(selectors)

	if got := strayPIDs(procs); len(got) != 0 {
		t.Fatalf("a pidfile-named process must not be a stray: %v", got)
	}
}

// Without a backend PID source there is no control-group attribution to reason
// about, so nothing can be a stray however the selectors are written.
func TestDiscoverStrayNeedsBackendAttribution(t *testing.T) {
	d := Discoverer{
		Reader:      strayCgroup(),
		ResolveUser: fakeUsers(map[string]uint32{"root": 0}),
	}
	selectors := []Selector{{Name: "daemon", Type: SelectorCommandMatch, Exe: "/usr/bin/daemon", User: "root"}}
	procs, _ := d.Discover(selectors)

	if got := strayPIDs(procs); len(got) != 0 {
		t.Fatalf("selector-only discovery must produce no strays: %v", got)
	}
}

// The backend names the principal first. When it names none and the first live
// cgroup member is a leftover, the classification loses that one — it must never
// gain one, because only a stray can be reaped.
func TestDiscoverStrayUnderReportsWhenNoPrincipalIsNamed(t *testing.T) {
	d := strayDiscoverer(strayCgroup(), 300, 400)
	procs, _ := d.Discover(nil)

	assertPIDs(t, "strays", strayPIDs(procs), []int{400})
}
