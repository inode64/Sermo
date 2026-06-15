package process

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

type fakeReader struct {
	ids map[int]Identity
}

func (r fakeReader) PIDs() ([]int, error) {
	pids := make([]int, 0, len(r.ids))
	for pid := range r.ids {
		pids = append(pids, pid)
	}
	return pids, nil
}

func (r fakeReader) Identity(pid int) (Identity, bool) {
	id, ok := r.ids[pid]
	return id, ok
}

type countingReader struct {
	fakeReader
	pidCalls int
}

func (r *countingReader) PIDs() ([]int, error) {
	r.pidCalls++
	return r.fakeReader.PIDs()
}

func fakeUsers(m map[string]uint32) UserResolver {
	return func(name string) (uint32, bool) {
		uid, ok := m[name]
		return uid, ok
	}
}

func pidsOf(procs []Process) []int {
	out := make([]int, len(procs))
	for i, p := range procs {
		out[i] = p.PID
	}
	sort.Ints(out)
	return out
}

func TestDiscoverCommandMatchExeAndUser(t *testing.T) {
	reader := fakeReader{ids: map[int]Identity{
		100: {PID: 100, PPID: 1, UID: 110, User: "mysql", Exe: "/opt/sermo-test/mysqld", ExeOK: true},
		101: {PID: 101, PPID: 1, UID: 999, User: "root", Exe: "/opt/sermo-test/mysqld", ExeOK: true},       // wrong user
		102: {PID: 102, PPID: 1, UID: 110, User: "mysql", Exe: "/opt/sermo-test/mysqld-fake", ExeOK: true}, // wrong exe (no substring)
	}}
	d := Discoverer{Reader: reader, ResolveUser: fakeUsers(map[string]uint32{"mysql": 110})}

	procs, warns := d.Discover([]Selector{
		{Name: "main", Type: SelectorCommandMatch, Exe: "/opt/sermo-test/mysqld", User: "mysql"},
	})
	if len(warns) != 0 {
		t.Fatalf("warnings = %v", warns)
	}
	if got := pidsOf(procs); len(got) != 1 || got[0] != 100 {
		t.Fatalf("matched pids = %v, want [100]", got)
	}
	if procs[0].Role != "main" || procs[0].Source != sourceCommand {
		t.Errorf("role/source = %q/%q", procs[0].Role, procs[0].Source)
	}
}

func TestDiscoverEmptyInputsAvoidSnapshot(t *testing.T) {
	reader := &countingReader{fakeReader: fakeReader{ids: map[int]Identity{100: {PID: 100}}}}
	d := Discoverer{Reader: reader}

	procs, warns := d.Discover(nil)
	if len(procs) != 0 || len(warns) != 0 {
		t.Fatalf("Discover(nil) = %+v, %v; want empty", procs, warns)
	}
	if reader.pidCalls != 0 {
		t.Fatalf("PIDs called %d times, want 0", reader.pidCalls)
	}
}

func TestDiscoverEmptyBackendPIDsAvoidSnapshot(t *testing.T) {
	reader := &countingReader{fakeReader: fakeReader{ids: map[int]Identity{100: {PID: 100}}}}
	d := Discoverer{
		Reader:      reader,
		BackendPIDs: func() []int { return []int{0, -1, 0} },
	}

	procs, warns := d.Discover(nil)
	if len(procs) != 0 || len(warns) != 0 {
		t.Fatalf("Discover(empty backend) = %+v, %v; want empty", procs, warns)
	}
	if reader.pidCalls != 0 {
		t.Fatalf("PIDs called %d times, want 0", reader.pidCalls)
	}
}

func TestDiscoverUnresolvableExeNeverMatches(t *testing.T) {
	reader := fakeReader{ids: map[int]Identity{
		100: {PID: 100, PPID: 1, UID: 110, ExeOK: false}, // exe unreadable / deleted
	}}
	d := Discoverer{Reader: reader, ResolveUser: fakeUsers(map[string]uint32{"mysql": 110})}

	procs, _ := d.Discover([]Selector{
		{Name: "main", Type: SelectorCommandMatch, Exe: "/opt/sermo-test/mysqld"},
	})
	if len(procs) != 0 {
		t.Fatalf("unresolvable exe must not match, got %v", pidsOf(procs))
	}
}

func TestDiscoverCommandMatchRequiresExeAndUser(t *testing.T) {
	reader := fakeReader{ids: map[int]Identity{
		100: {PID: 100, PPID: 1, UID: 110, User: "mysql", ExeOK: false},
	}}
	d := Discoverer{Reader: reader, ResolveUser: fakeUsers(map[string]uint32{"mysql": 110})}

	procs, _ := d.Discover([]Selector{{Name: "u", Type: SelectorCommandMatch, User: "mysql"}})
	if got := pidsOf(procs); len(got) != 0 {
		t.Fatalf("user-only command_match pids = %v, want none", got)
	}
}

func TestDiscoverBuildsProcessTree(t *testing.T) {
	reader := fakeReader{ids: map[int]Identity{
		100: {PID: 100, PPID: 1, UID: 110, Exe: "/opt/sermo-test/mysqld", ExeOK: true},
		200: {PID: 200, PPID: 100, UID: 110, Exe: "/opt/sermo-test/mysqld", ExeOK: true},
		300: {PID: 300, PPID: 200, UID: 110, Exe: "/bin/sh", ExeOK: true},
		400: {PID: 400, PPID: 1, UID: 0, Exe: "/sbin/init", ExeOK: true},
	}}
	d := Discoverer{Reader: reader, ResolveUser: fakeUsers(map[string]uint32{"mysql": 110})}

	procs, _ := d.Discover([]Selector{{Name: "main", Type: SelectorCommandMatch, Exe: "/opt/sermo-test/mysqld", User: "mysql"}})
	got := pidsOf(procs)
	want := []int{100, 200, 300}
	if len(got) != len(want) {
		t.Fatalf("tree pids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tree pids = %v, want %v", got, want)
		}
	}
	// 200 matched directly by exe; 300 only via the tree.
	for _, p := range procs {
		if p.PID == 300 && p.Source != sourceChild {
			t.Errorf("pid 300 source = %q, want child", p.Source)
		}
	}
}

func TestDiscoverPidfileCandidates(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "real.pid")
	if err := os.WriteFile(good, []byte("100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "absent.pid") // never created
	reader := fakeReader{ids: map[int]Identity{
		100: {PID: 100, PPID: 1, UID: 110, Exe: "/opt/sermo-test/mysqld", ExeOK: true},
	}}
	d := Discoverer{Reader: reader}

	// First candidate is absent; discovery falls through to the second, no warning.
	procs, warns := d.Discover([]Selector{{Name: "pidfile", Type: SelectorPidfile, Paths: []string{missing, good}}})
	if len(warns) != 0 {
		t.Fatalf("a working fallback candidate must not warn: %v", warns)
	}
	if got := pidsOf(procs); len(got) != 1 || got[0] != 100 {
		t.Fatalf("pids = %v, want [100]", got)
	}

	// No candidate works -> exactly one (last) warning.
	if _, warns := d.Discover([]Selector{{Name: "pidfile", Type: SelectorPidfile, Paths: []string{missing, missing}}}); len(warns) != 1 {
		t.Errorf("all-missing candidates should yield one warning, got %v", warns)
	}
}

func TestDiscoverPidfile(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "mysqld.pid")
	if err := os.WriteFile(pidfile, []byte("100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reader := fakeReader{ids: map[int]Identity{
		100: {PID: 100, PPID: 1, UID: 110, User: "mysql", Exe: "/opt/sermo-test/mysqld", ExeOK: true},
	}}
	d := Discoverer{Reader: reader}

	procs, warns := d.Discover([]Selector{{Name: "pidfile", Type: SelectorPidfile, Paths: []string{pidfile}}})
	if len(warns) != 0 {
		t.Fatalf("warnings = %v", warns)
	}
	if got := pidsOf(procs); len(got) != 1 || got[0] != 100 {
		t.Fatalf("pidfile pids = %v, want [100]", got)
	}
	if procs[0].Source != sourcePidfile {
		t.Errorf("source = %q, want pidfile", procs[0].Source)
	}
}

func TestDiscoverPidfileDeadPIDWarns(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "mysqld.pid")
	if err := os.WriteFile(pidfile, []byte("999"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := Discoverer{Reader: fakeReader{ids: map[int]Identity{}}}

	procs, warns := d.Discover([]Selector{{Name: "pidfile", Type: SelectorPidfile, Paths: []string{pidfile}}})
	if len(procs) != 0 {
		t.Fatalf("dead pid should yield no process, got %v", pidsOf(procs))
	}
	if len(warns) != 1 {
		t.Fatalf("expected 1 warning, got %v", warns)
	}
}

func TestDiscoverSuppressesPidfileWarningWhenBackendFoundProcess(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.pid")
	reader := fakeReader{ids: map[int]Identity{
		100: {PID: 100, PPID: 1, UID: 110, Exe: "/usr/bin/node_exporter", ExeOK: true},
	}}
	d := Discoverer{
		Reader:      reader,
		BackendPIDs: func() []int { return []int{100} },
	}

	procs, warns := d.Discover([]Selector{{Name: "pidfile", Type: SelectorPidfile, Paths: []string{missing}}})
	if len(warns) != 0 {
		t.Fatalf("warnings = %v, want none because backend found the process", warns)
	}
	if got := pidsOf(procs); len(got) != 1 || got[0] != 100 {
		t.Fatalf("pids = %v, want [100]", got)
	}
}

func TestDiscoverBackendMainPIDSeedsTree(t *testing.T) {
	reader := fakeReader{ids: map[int]Identity{
		100: {PID: 100, PPID: 1, UID: 110, Exe: "/usr/sbin/mysqld", ExeOK: true},
		200: {PID: 200, PPID: 100, UID: 110, Exe: "/bin/sh", ExeOK: true}, // child of MainPID
		400: {PID: 400, PPID: 1, UID: 0, Exe: "/sbin/init", ExeOK: true},  // unrelated
	}}
	d := Discoverer{
		Reader:      reader,
		ResolveUser: fakeUsers(nil),
		BackendPIDs: func() []int { return []int{100} },
	}

	procs, _ := d.Discover(nil) // no selectors: backend MainPID is the only seed
	got := pidsOf(procs)
	if len(got) != 2 || got[0] != 100 || got[1] != 200 {
		t.Fatalf("pids = %v, want [100 200] (MainPID + child)", got)
	}
	for _, p := range procs {
		if p.PID == 100 && p.Source != sourceBackend {
			t.Errorf("MainPID source = %q, want backend", p.Source)
		}
	}
}

func TestDiscoverMainPIDDedupedWithSelector(t *testing.T) {
	reader := fakeReader{ids: map[int]Identity{
		100: {PID: 100, PPID: 1, UID: 110, Exe: testExe, ExeOK: true},
	}}
	d := Discoverer{
		Reader:      reader,
		ResolveUser: fakeUsers(map[string]uint32{"mysql": 110}),
		BackendPIDs: func() []int { return []int{100} },
	}
	// The same PID is found by MainPID and command_match; it appears once,
	// keeping the backend source (found first).
	procs, _ := d.Discover([]Selector{{Name: "m", Type: SelectorCommandMatch, Exe: testExe, User: "mysql"}})
	if len(procs) != 1 || procs[0].Source != sourceBackend {
		t.Fatalf("procs = %+v, want one process from the backend source", procs)
	}
}

func TestObserveState(t *testing.T) {
	d := func(ids map[int]Identity) Discoverer {
		return Discoverer{Reader: fakeReader{ids: ids}, ResolveUser: fakeUsers(map[string]uint32{"mysql": 110})}
	}

	running := d(map[int]Identity{100: {PID: 100, UID: 110, Exe: testExe, ExeOK: true, State: "S"}})
	if got := running.ObserveState(testExe, "mysql"); got != StateRunning {
		t.Errorf("ObserveState = %q, want running", got)
	}

	zombie := d(map[int]Identity{100: {PID: 100, UID: 110, Exe: testExe, ExeOK: true, State: "Z"}})
	if got := zombie.ObserveState(testExe, "mysql"); got != StateZombie {
		t.Errorf("ObserveState = %q, want zombie", got)
	}

	absent := d(map[int]Identity{100: {PID: 100, UID: 999, Exe: testExe, ExeOK: true, State: "S"}})
	if got := absent.ObserveState(testExe, "mysql"); got != StateAbsent {
		t.Errorf("ObserveState = %q, want absent (wrong user)", got)
	}

	unresolved := d(map[int]Identity{100: {PID: 100, UID: 110, ExeOK: false, State: "S"}})
	if got := unresolved.ObserveState(testExe, "mysql"); got != StateAbsent {
		t.Errorf("ObserveState = %q, want absent (unresolvable exe)", got)
	}
}

func TestParseSelectors(t *testing.T) {
	tree := map[string]any{
		"processes": map[string]any{
			"pidfile": map[string]any{"type": "pidfile", "path": "/run/x.pid"},
			"command": map[string]any{"type": "command_match", "exe": "/opt/sermo-test/mysqld", "user": "mysql"},
			"bogus":   map[string]any{"type": "weird"},
			"nopath":  map[string]any{"type": "pidfile"},
			"user":    map[string]any{"type": "command_match", "user": "mysql"},
		},
	}
	selectors, warnings := ParseSelectors(tree)
	if len(selectors) != 2 {
		t.Fatalf("selectors = %+v, want 2 valid", selectors)
	}
	if len(warnings) != 3 {
		t.Fatalf("warnings = %v, want 3 (bogus type, missing path, partial command_match)", warnings)
	}
}

func TestParseSelectorsCanonicalizesExe(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "mysqld")
	link := filepath.Join(dir, "mysqld-link")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	tree := map[string]any{
		"processes": map[string]any{
			"main": map[string]any{"type": "command_match", "exe": link},
		},
	}

	selectors, warnings := ParseSelectors(tree)
	if len(warnings) != 0 || len(selectors) != 1 {
		t.Fatalf("selectors=%+v warnings=%v, want one selector", selectors, warnings)
	}
	if selectors[0].exePath != target {
		t.Fatalf("exePath = %q, want %q", selectors[0].exePath, target)
	}
}

func TestMatchesCmdAndGroup(t *testing.T) {
	d := Discoverer{
		ResolveUser:  func(string) (uint32, bool) { return 1000, true },
		ResolveGroup: func(string) (uint32, bool) { return 2000, true },
	}
	id := Identity{PID: 7, UID: 1000, GID: 2000, ExeOK: true, Exe: "/usr/bin/java", Cmdline: []string{"java", "-jar", "/opt/unifi/lib/ace.jar"}}

	// cmd regex matches the joined cmdline; user/group also AND-checked.
	if !d.matches(Selector{Type: "command_match", Cmd: "unifi", User: "u", Group: "g"}, id, d.ResolveUser) {
		t.Fatal("cmd+user+group selector should match")
	}
	// cmd that does not appear in cmdline must not match.
	if d.matches(Selector{Type: "command_match", Cmd: "postgres"}, id, d.ResolveUser) {
		t.Fatal("non-matching cmd must not match")
	}
	// group mismatch fails even when cmd matches.
	d2 := Discoverer{ResolveUser: d.ResolveUser, ResolveGroup: func(string) (uint32, bool) { return 9999, true }}
	if d2.matches(Selector{Type: "command_match", Cmd: "unifi", Group: "g"}, id, d2.ResolveUser) {
		t.Fatal("group mismatch must fail the match")
	}
	// a selector with neither exe nor cmd never matches.
	if d.matches(Selector{Type: "command_match", User: "u"}, id, d.ResolveUser) {
		t.Fatal("user-only selector must never match")
	}
}
