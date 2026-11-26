package process

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/servicemgr"
)

func TestObserveDistinguishesAbsenceFromUncertainty(t *testing.T) {
	t.Parallel()
	main := Selector{Name: RoleMain, Type: SelectorCommandMatch, Exe: "/opt/apache", User: "root"}
	worker := Selector{Name: "worker", Type: SelectorCommandMatch, Exe: "/opt/apache", User: "apache"}
	tests := []struct {
		name           string
		ids            map[int]Identity
		selectors      []Selector
		backend        []int
		trusted, known bool
		replaced       bool
		count          int
	}{
		{name: "confirmed absence", selectors: []Selector{main}, known: true},
		{name: "no selectors", known: false},
		{name: "unresolved user", selectors: []Selector{{Type: SelectorCommandMatch, Exe: "/opt/apache", User: "unknown"}}},
		{name: "command alone cannot prove absence", selectors: []Selector{{Type: SelectorCommandMatch, Cmd: "apache"}}},
		{name: "live main", selectors: []Selector{main}, ids: map[int]Identity{100: {PID: 100, UID: 0, Exe: "/opt/apache", ExeOK: true}}, trusted: true, known: true, count: 1},
		{name: "unreadable attributed executable", selectors: []Selector{main}, backend: []int{100}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1}}, count: 1},
		{name: "unreadable unseeded executable", selectors: []Selector{main}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1}}},
		{name: "deleted unseeded executable survives", selectors: []Selector{main}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1, ExePrev: "/opt/apache"}}, replaced: true, known: true, count: 1},
		{name: "deleted worker is not main evidence", selectors: []Selector{main, worker}, backend: []int{100}, ids: map[int]Identity{100: {PID: 100, UID: 33, PPID: 1, ExePrev: "/opt/apache"}}, known: true, count: 1},
		{name: "unrelated attributed deleted exe", selectors: []Selector{main}, backend: []int{100}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1, ExePrev: "/opt/unrelated"}}, known: true, count: 1},
		{name: "deleted zombie is absent", selectors: []Selector{main}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1, State: ProcStateZombie, ExePrev: "/opt/apache"}}, known: true},
		{name: "zombie backend seed cannot hide live main", selectors: []Selector{main}, backend: []int{100}, ids: map[int]Identity{
			100: {PID: 100, UID: 0, PPID: 1, State: ProcStateZombie},
			101: {PID: 101, UID: 0, PPID: 1, Exe: "/opt/apache", ExeOK: true},
		}, trusted: true, known: true, count: 1},
		{name: "worker is not the main", selectors: []Selector{main, worker}, ids: map[int]Identity{100: {PID: 100, UID: 33, Exe: "/opt/apache", ExeOK: true}}, known: true, count: 1},
		{name: "kernel thread is not uncertainty", selectors: []Selector{main}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 2}}, known: true},
		{name: "unrelated replaced binary", selectors: []Selector{main}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1, ExePrev: "/opt/unrelated"}}, known: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := Discoverer{Reader: fakeReader{ids: tt.ids}, ResolveUser: fakeUsers(map[string]uint32{"root": 0, "apache": 33}), BackendPIDs: func() []int { return tt.backend }}
			out, err := d.Observe(tt.selectors)
			if err != nil || out.Trusted != tt.trusted || out.ReplacedExecutable != tt.replaced || out.AbsenceKnown != tt.known || len(out.Processes) != tt.count {
				t.Fatalf("observation=%+v err=%v; want trusted=%v known=%v count=%d", out, err, tt.trusted, tt.known, tt.count)
			}
		})
	}
}

func TestObserveRejectsIncompleteSnapshot(t *testing.T) {
	t.Parallel()
	d := Discoverer{Reader: &errorAwareSnapshotReader{err: errors.New("permission denied")}}
	if _, err := d.Observe([]Selector{{Type: SelectorCommandMatch, Exe: "/opt/apache", User: "root"}}); err == nil {
		t.Fatal("an unreadable process table must not prove absence")
	}
}

func TestObserveTrackedWithoutSelectorsOrBackendPIDs(t *testing.T) {
	t.Parallel()
	old := []Process{{PID: 100, StartTicks: 10, Source: SourceBackend}}
	for _, tc := range []struct {
		name     string
		ids      map[int]Identity
		wantLive bool
	}{
		{name: "exited", ids: map[int]Identity{}},
		{name: "escaped attribution", ids: map[int]Identity{100: {PID: 100, StartTicks: 10, StartTicksOK: true}}, wantLive: true},
		{name: "PID reused", ids: map[int]Identity{100: {PID: 100, StartTicks: 20, StartTicksOK: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := Discoverer{Reader: fakeReader{ids: tc.ids}}
			out, err := d.ObserveTracked(nil, old)
			if err != nil || (len(out.Processes) > 0) != tc.wantLive || out.AbsenceKnown {
				t.Fatalf("tracked=%+v error=%v", out, err)
			}
			if err := out.VerifyExited(old); (err != nil) != tc.wantLive {
				t.Fatalf("exit proof=%v, survivor=%v", err, tc.wantLive)
			}
		})
	}
	d := Discoverer{Reader: &errorAwareSnapshotReader{err: errors.New("unreadable snapshot")}}
	if _, err := d.ObserveTracked(nil, old); err == nil {
		t.Fatal("tracked exit must reject an incomplete snapshot")
	}
	if _, err := d.ObserveTracked(nil, nil); err != nil {
		t.Fatal("unconfigured monitoring must keep its no-scan fast path")
	}
}

func TestObserveUsesOneFreshSnapshot(t *testing.T) {
	t.Parallel()
	inner := &countingReader{ids: map[int]Identity{100: {PID: 100, UID: 0, Exe: "/opt/apache", ExeOK: true}}}
	d := Discoverer{Reader: NewCachingReader(inner, 0), ResolveUser: fakeUsers(map[string]uint32{"root": 0})}
	selectors := []Selector{{Type: SelectorCommandMatch, Exe: "/opt/apache", User: "root"}}
	for range 2 {
		if _, err := d.Observe(selectors); err != nil {
			t.Fatal(err)
		}
	}
	if inner.pidCalls != 2 {
		t.Fatalf("snapshot walks=%d, want one per observation", inner.pidCalls)
	}
}

func TestObserveCachedSharesMonitoringSnapshot(t *testing.T) {
	inner := &countingReader{ids: map[int]Identity{100: {PID: 100, UID: 0, Exe: "/opt/apache", ExeOK: true}}}
	d := Discoverer{Reader: NewCachingReader(inner, time.Hour), ResolveUser: fakeUsers(map[string]uint32{"root": 0})}
	selectors := []Selector{{Type: SelectorCommandMatch, Exe: "/opt/apache", User: "root"}}
	for range 2 {
		if _, err := d.ObserveCached(selectors); err != nil {
			t.Fatal(err)
		}
	}
	if inner.pidCalls != 1 {
		t.Fatalf("monitoring repeated host scan %d times", inner.pidCalls)
	}
	if _, err := d.Observe(selectors); err != nil {
		t.Fatal(err)
	}
	if inner.pidCalls != 2 {
		t.Fatal("operation reused monitoring snapshot")
	}
}

func TestObserveKeepsZombiesInSharedMonitoringSnapshot(t *testing.T) {
	t.Parallel()
	reader := &errorAwareSnapshotReader{snapshot: map[int]Identity{
		100: {PID: 100, UID: 0, PPID: 1, State: ProcStateZombie},
		101: {PID: 101, UID: 0, PPID: 1, Exe: "/opt/apache", ExeOK: true},
	}}
	d := Discoverer{Reader: reader, ResolveUser: fakeUsers(map[string]uint32{"root": 0}), BackendPIDs: func() []int { return []int{100} }}
	out, err := d.Observe([]Selector{{Type: SelectorCommandMatch, Exe: "/opt/apache", User: "root"}})
	if err != nil || !out.Trusted || len(out.Processes) != 1 || out.Processes[0].PID != 101 {
		t.Fatalf("observation=%+v err=%v", out, err)
	}
	if len(reader.snapshot) != 2 || reader.snapshot[100].State != ProcStateZombie {
		t.Fatalf("operation mutated the monitoring snapshot: %+v", reader.snapshot)
	}
}

func TestObservationVerifiesExitedGenerations(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		ids      map[int]Identity
		oldTicks uint64
		wantErr  bool
	}{
		{name: "exited", oldTicks: 10, ids: map[int]Identity{}},
		{name: "missing snapshot", oldTicks: 10, wantErr: true},
		{name: "missing old generation", ids: map[int]Identity{}, wantErr: true},
		{name: "unchanged outside unit", oldTicks: 10, ids: map[int]Identity{100: {PID: 100, StartTicks: 10, StartTicksOK: true}}, wantErr: true},
		{name: "reused PID", oldTicks: 10, ids: map[int]Identity{100: {PID: 100, StartTicks: 20, StartTicksOK: true}}},
		{name: "unknown current generation", oldTicks: 10, ids: map[int]Identity{100: {PID: 100}}, wantErr: true},
		{name: "zombie exited", oldTicks: 10, ids: map[int]Identity{100: {PID: 100, State: ProcStateZombie, StartTicks: 10, StartTicksOK: true}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := Observation{snapshot: withoutZombieIdentities(tt.ids)}
			err := out.VerifyExited([]Process{{PID: 100, StartTicks: tt.oldTicks}})
			if (err != nil) != tt.wantErr {
				t.Fatalf("VerifyExited()=%v, want error=%v", err, tt.wantErr)
			}
		})
	}
}

// A backend query that did not answer leaves absence unproven: a unit member
// no selector describes is visible only in the backend's set.
func TestObserveTreatsAFailedBackendQueryAsIncomplete(t *testing.T) {
	t.Parallel()
	inner := fakeReader{ids: map[int]Identity{4242: {PID: 4242, PPID: 1, UID: 0, Exe: "/usr/bin/leftover", ExeOK: true}}}
	queryErr := errors.New("systemctl show MainPID: timeout")
	strict := []Selector{{Name: "main", Type: SelectorCommandMatch, Exe: "/usr/sbin/acpid", User: "root"}}
	for _, selectors := range [][]Selector{nil, strict} {
		d := Discoverer{
			Reader:           inner,
			ResolveUser:      fakeUsers(map[string]uint32{"root": 0}),
			FreshBackendPIDs: func() ([]int, error) { return nil, queryErr },
		}
		for name, observe := range map[string]func() (Observation, error){
			"Observe":        func() (Observation, error) { return d.Observe(selectors) },
			"ObserveTracked": func() (Observation, error) { return d.ObserveTracked(selectors, nil) },
		} {
			observation, err := observe()
			if err == nil || observation.AbsenceKnown || !strings.Contains(err.Error(), "backend PID query") {
				t.Fatalf("%s with %d selectors = %+v, %v; want an incomplete-discovery error", name, len(selectors), observation, err)
			}
		}
	}
}

// Monitoring discovery reads the shared backend answer; the observations an
// operation acts on read the backend live.
func TestObserveReadsBackendPIDsLive(t *testing.T) {
	t.Parallel()
	inner := fakeReader{ids: map[int]Identity{100: {PID: 100, PPID: 1, UID: 0, Exe: "/opt/apache", ExeOK: true}}}
	var cached, live int
	d := Discoverer{
		Reader:           inner,
		ResolveUser:      fakeUsers(map[string]uint32{"root": 0}),
		BackendPIDs:      func() []int { cached++; return []int{100} },
		FreshBackendPIDs: func() ([]int, error) { live++; return []int{100}, nil },
	}
	if procs, _ := d.Discover(nil); len(procs) != 1 {
		t.Fatalf("Discover = %+v, want the backend process", procs)
	}
	if _, err := d.ObserveCached(nil); err != nil {
		t.Fatal(err)
	}
	if cached != 2 || live != 0 {
		t.Fatalf("monitoring reads: cached=%d live=%d, want 2 and 0", cached, live)
	}
	for _, observe := range []func() (Observation, error){
		func() (Observation, error) { return d.Observe(nil) },
		func() (Observation, error) { return d.ObserveTracked(nil, nil) },
	} {
		observation, err := observe()
		if err != nil {
			t.Fatal(err)
		}
		if len(observation.Processes) != 1 {
			t.Fatalf("observation = %+v, want the backend process", observation)
		}
	}
	if cached != 2 || live != 2 {
		t.Fatalf("operation reads: cached=%d live=%d, want 2 and 2", cached, live)
	}
}

// The operation's fresh observation and the monitor's forecast over the
// cycle's discovery reach the same verdict on the same process table.
func TestPrincipalIdentityUnverified(t *testing.T) {
	t.Parallel()
	asAlloy := Selector{Name: RoleMain, Type: SelectorCommandMatch, Exe: "/usr/bin/alloy", User: "alloy"}
	asRoot := Selector{Name: RoleMain, Type: SelectorCommandMatch, Exe: "/usr/bin/alloy", User: "root"}
	alloy := map[int]Identity{100: {PID: 100, UID: 0, PPID: 1, Exe: "/usr/bin/alloy", ExeOK: true}}
	replacedAlloy := map[int]Identity{100: {PID: 100, UID: 0, PPID: 1, ExePrev: "/usr/bin/alloy"}}
	main := Selector{Name: RoleMain, Type: SelectorCommandMatch, Exe: "/opt/apache", User: "root"}
	worker := Selector{Name: "worker", Type: SelectorCommandMatch, Exe: "/opt/apache", User: "apache"}
	dropped := Selector{Name: RoleMain, Type: SelectorCommandMatch, Exe: "/opt/statd", User: "nobody"}
	kept := Selector{Name: "main-root", Type: SelectorCommandMatch, Exe: "/opt/statd", User: "root"}
	tests := []struct {
		name       string
		selectors  []Selector
		ids        map[int]Identity
		unverified bool
	}{
		{name: "unit runs the daemon as another user", selectors: []Selector{asAlloy}, ids: alloy, unverified: true},
		{name: "replaced binary under another user", selectors: []Selector{asAlloy}, ids: replacedAlloy, unverified: true},
		{name: "matching user", selectors: []Selector{asRoot}, ids: alloy},
		{name: "replaced binary of the matching user", selectors: []Selector{asRoot}, ids: replacedAlloy},
		{name: "no strict selector to judge by", selectors: []Selector{{Name: RoleMain, Type: SelectorCommandMatch, Cmd: "alloy"}}, ids: alloy},
		{name: "delegated selector is observe-only", selectors: []Selector{{Name: RoleMain, Type: SelectorCommandMatch, Exe: "/usr/bin/alloy", User: "alloy", Delegated: true}}, ids: alloy},
		{name: "worker is not the main", selectors: []Selector{main, worker}, ids: map[int]Identity{100: {PID: 100, UID: 33, PPID: 1, Exe: "/opt/apache", ExeOK: true}}, unverified: true},
		{name: "deleted worker is not main evidence", selectors: []Selector{main, worker}, ids: map[int]Identity{100: {PID: 100, UID: 33, PPID: 1, ExePrev: "/opt/apache"}}, unverified: true},
		{name: "unrelated attributed deleted exe", selectors: []Selector{main}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1, ExePrev: "/opt/unrelated"}}, unverified: true},
		{name: "replaced main outside the attributed tree", selectors: []Selector{main}, ids: map[int]Identity{
			100: {PID: 100, UID: 33, PPID: 1, Exe: "/opt/other", ExeOK: true},
			200: {PID: 200, UID: 0, PPID: 1, ExePrev: "/opt/apache"},
		}},
		{name: "main under the second user its profile declares", selectors: []Selector{dropped, kept}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1, Exe: "/opt/statd", ExeOK: true}}},
		{name: "replaced main under the second user its profile declares", selectors: []Selector{dropped, kept}, ids: map[int]Identity{100: {PID: 100, UID: 0, PPID: 1, ExePrev: "/opt/statd"}}},
		{name: "replaced worker with nothing attributed", selectors: []Selector{main, worker}, ids: map[int]Identity{
			200: {PID: 200, UID: 33, PPID: 1, ExePrev: "/opt/apache"},
		}, unverified: true},
	}
	users := fakeUsers(map[string]uint32{"root": 0, "apache": 33, "alloy": 993, "nobody": 65534})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := Discoverer{Reader: fakeReader{ids: tt.ids}, ResolveUser: users, BackendPIDs: func() []int { return []int{100} }}
			out, err := d.Observe(tt.selectors)
			if err != nil {
				t.Fatal(err)
			}
			if got := out.Principal().Unverified(); got != tt.unverified {
				t.Fatalf("operation principal=%+v unverified=%v, want %v", out.Principal(), got, tt.unverified)
			}
			discovered, _ := d.Discover(tt.selectors)
			forecast := d.PrincipalIdentityIn(discovered, tt.selectors)
			if got := forecast.Unverified(); got != tt.unverified {
				t.Fatalf("monitor forecast unverified=%v, want %v", got, tt.unverified)
			}
			// Only an unverified identity carries evidence, and it always does.
			for path, detail := range map[string]string{"operation": out.Principal().Detail, "forecast": forecast.Detail} {
				if (detail != "") != tt.unverified {
					t.Fatalf("%s detail = %q, want evidence only when unverified (%v)", path, detail, tt.unverified)
				}
			}
		})
	}
}

// The evidence of an unverified identity names both sides: what each strict
// main selector requires and the processes the unit really runs.
func TestPrincipalIdentityDetailNamesTheSelectorAndTheProcessesFound(t *testing.T) {
	t.Parallel()
	launcher := Selector{Name: RoleMain, Type: SelectorCommandMatch, Exe: "/usr/bin/pm2", User: "root", Cmd: "^PM2 v"}
	ids := map[int]Identity{100: {PID: 100, UID: 0, PPID: 1, Exe: "/usr/bin/node", ExeOK: true}}
	d := Discoverer{Reader: fakeReader{ids: ids}, ResolveUser: fakeUsers(map[string]uint32{"root": 0}), BackendPIDs: func() []int { return []int{100} }}
	out, err := d.Observe([]Selector{launcher})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := out.Principal().Detail, "main wants exe=/usr/bin/pm2 user=root cmd=^PM2 v; running: pid 100 exe=/usr/bin/node user=0"; got != want {
		t.Fatalf("detail = %q, want %q", got, want)
	}

	// A unit with many workers shows a few, by name when the user resolved.
	found := make([]Process, 0, 5)
	for pid := 200; pid < 205; pid++ {
		found = append(found, Process{PID: pid, User: "www", Exe: "/usr/bin/node", ExeOK: pid != 201})
	}
	got := identityMismatch([]Selector{launcher}, found)
	for _, want := range []string{"pid 200 exe=/usr/bin/node user=www", "pid 201 exe=(unreadable) user=www", "pid 202", "2 more"} {
		if !strings.Contains(got, want) {
			t.Fatalf("mismatch = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "pid 203") {
		t.Fatalf("mismatch = %q, want the process list bounded", got)
	}
}

// A delegated process stands for the service only when no selector names one of
// its own. It never counts for a service with a principal to judge, even a
// pidfile, and absence stays absence.
func TestObservationAttributesDelegatedProcessOnlyWhenObserveOnly(t *testing.T) {
	t.Parallel()
	guest := Selector{Name: RoleMain, Type: SelectorCommandMatch, Cmd: "qemu", Delegated: true}
	daemon := Selector{Name: "daemon", Type: SelectorCommandMatch, Exe: "/opt/sermo-test/daemon", User: "root"}
	pidfile := Selector{Name: SelectorPidfile, Type: SelectorPidfile, Paths: []string{filepath.Join(t.TempDir(), "absent.pid")}}
	emulator := map[int]Identity{100: {PID: 100, UID: 77, PPID: 1, Exe: "/opt/sermo-test/qemu", ExeOK: true, Cmdline: []string{"qemu"}}}
	for _, tc := range []struct {
		name       string
		selectors  []Selector
		ids        map[int]Identity
		attributed bool
	}{
		{name: "every selector delegated", selectors: []Selector{guest}, ids: emulator, attributed: true},
		{name: "every selector delegated, nothing running", selectors: []Selector{guest}},
		{name: "a principal selector declared", selectors: []Selector{guest, daemon}, ids: emulator},
		{name: "a pidfile declared", selectors: []Selector{guest, pidfile}, ids: emulator},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := Discoverer{Reader: fakeReader{ids: tc.ids}, ResolveUser: fakeUsers(map[string]uint32{"root": 0})}
			out, err := d.Observe(tc.selectors)
			if err != nil {
				t.Fatal(err)
			}
			principal := out.Principal()
			if principal.Attributed != tc.attributed || principal.Unverified() {
				t.Fatalf("principal = %+v, want attributed %v and never unverified", principal, tc.attributed)
			}
		})
	}
}

// A process the cycle discovered that has since left the shared snapshot is no
// evidence either way: it must not forecast a refusal for a healthy service.
func TestPrincipalIdentityInIgnoresAVanishedProcess(t *testing.T) {
	t.Parallel()
	main := Selector{Name: RoleMain, Type: SelectorCommandMatch, Exe: "/opt/apache", User: "root"}
	d := Discoverer{Reader: fakeReader{ids: map[int]Identity{}}, ResolveUser: fakeUsers(map[string]uint32{"root": 0})}
	got := d.PrincipalIdentityIn([]Process{{PID: 100, Role: RoleMain, Source: SourceBackend}}, []Selector{main})
	if got.Attributed || got.Unverified() {
		t.Fatalf("vanished process principal = %+v, want no evidence", got)
	}
}

// A daemon's forked sessions outlive its stop on the binary an upgrade
// replaced, hanging off PID 1: a fresh look refuses them as competing
// instances. A generation the earlier observation attributed is a tracked
// survivor instead, round after round; one first seen later keeps the refusal.
func TestObserveTrackedRetainsAttributedReplacedBinarySessions(t *testing.T) {
	const cgroup = "0::/openrc.exim"
	selectors := []Selector{{Name: RoleMain, Type: SelectorCommandMatch, Exe: testExe, User: "root"}}
	deleted := func(pid, ppid int, ticks uint64) Identity {
		return Identity{PID: pid, PPID: ppid, UID: 0, ExePrev: testExe, ExeFile: ExecutableFile{Device: 1, Inode: 7},
			StartTicks: ticks, StartTicksOK: true, Cgroup: cgroup}
	}
	d := Discoverer{
		Reader:           fakeReader{ids: map[int]Identity{10: deleted(10, 1, 100), 11: deleted(11, 10, 110), 12: deleted(12, 10, 120)}},
		ResolveUser:      fakeUsers(map[string]uint32{"root": 0}),
		BackendPIDs:      func() []int { return []int{10, 11, 12} },
		ProcessOwnership: func(id Identity) (bool, bool) { return servicemgr.CgroupOwnership(id.Cgroup, "exim") },
	}
	before, err := d.Observe(selectors)
	if err != nil {
		t.Fatal(err)
	}
	d.BackendPIDs = nil
	d.Reader = fakeReader{ids: map[int]Identity{11: deleted(11, 1, 110), 12: deleted(12, 1, 120), 13: deleted(13, 1, 130)}}
	tracked := before.Processes
	for round := range 2 {
		after, err := d.ObserveTracked(selectors, tracked)
		if err != nil {
			t.Fatal(err)
		}
		reasons := map[int]string{}
		for _, p := range after.Processes {
			reasons[p.PID] = p.SignalBlockReason
		}
		if reasons[11] != ReasonTrackedOutsideDiscovery || reasons[12] != ReasonTrackedOutsideDiscovery {
			t.Fatalf("round %d: sessions refused with %q, %q; want tracked survivors", round, reasons[11], reasons[12])
		}
		if late := reasons[13]; late == "" || late == ReasonTrackedOutsideDiscovery {
			t.Fatalf("round %d: late process reason %q, want the candidate's refusal", round, late)
		}
		// The operation lifts the tracked survivors' refusal on unit membership.
		tracked = after.Processes
		for i := range tracked {
			if tracked[i].SignalBlockReason == ReasonTrackedOutsideDiscovery {
				tracked[i].SignalBlockReason = ""
			}
		}
	}
}

// An earlier observation with an unknown start tick proves no generation: the
// process now at that PID may be a recycled one, so it keeps its refusal.
func TestObserveTrackedNeedsAProvenGenerationToRetainASession(t *testing.T) {
	const cgroup = "0::/openrc.exim"
	selectors := []Selector{{Name: RoleMain, Type: SelectorCommandMatch, Exe: testExe, User: "root"}}
	session := func(pid int, ticks uint64) Identity {
		return Identity{PID: pid, PPID: 1, UID: 0, ExePrev: testExe, ExeFile: ExecutableFile{Device: 1, Inode: 7},
			StartTicks: ticks, StartTicksOK: true, Cgroup: cgroup}
	}
	d := Discoverer{
		Reader:           fakeReader{ids: map[int]Identity{11: session(11, 110), 12: session(12, 120)}},
		ResolveUser:      fakeUsers(map[string]uint32{"root": 0}),
		ProcessOwnership: func(id Identity) (bool, bool) { return servicemgr.CgroupOwnership(id.Cgroup, "exim") },
	}
	previous := []Process{
		{PID: 11, UID: 0, ExePrev: testExe, ExeFile: ExecutableFile{Device: 1, Inode: 7}, Cgroup: cgroup},
		{PID: 12, UID: 0, ExePrev: testExe, ExeFile: ExecutableFile{Device: 1, Inode: 7}, StartTicks: 120, Cgroup: cgroup},
	}
	after, err := d.ObserveTracked(selectors, previous)
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[int]string{}
	for _, p := range after.Processes {
		reasons[p.PID] = p.SignalBlockReason
	}
	if reasons[11] == "" || reasons[11] == ReasonTrackedOutsideDiscovery {
		t.Fatalf("unknown generation reason %q, want the candidate's refusal", reasons[11])
	}
	if reasons[12] != ReasonTrackedOutsideDiscovery {
		t.Fatalf("proven generation reason %q, want a tracked survivor", reasons[12])
	}
}
