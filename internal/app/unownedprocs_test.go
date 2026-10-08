package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"sermo/internal/checks"
	"sermo/internal/notify"
	"sermo/internal/pkgdb"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
	"sermo/internal/severity"
)

// fakePackages answers package ownership from a fixed set.
type fakePackages struct {
	backend  pkgdb.Backend
	notOwned map[string]bool
	err      error
	queries  [][]string
}

func (f *fakePackages) Refresh(context.Context) error { return f.err }
func (f *fakePackages) Backend() pkgdb.Backend        { return f.backend }
func (f *fakePackages) OwnedAll(_ context.Context, exes []string) map[string]pkgdb.Ownership {
	f.queries = append(f.queries, exes)
	out := make(map[string]pkgdb.Ownership, len(exes))
	for _, exe := range exes {
		if f.backend == pkgdb.BackendNone {
			out[exe] = pkgdb.Unknown
		} else if f.notOwned[exe] {
			out[exe] = pkgdb.NotOwned
		} else {
			out[exe] = pkgdb.Owned
		}
	}
	return out
}

const (
	testUnownedNow   = 1_000_000
	testHostMountNS  = "mnt:[4026531840]"
	testOtherMountNS = "mnt:[4026532999]"
)

var testUnownedClock = time.Unix(testUnownedNow, 0)

// unownedProc is a sampled process old enough to be listed.
func unownedProc(pid int, exe, cgroup string) ProcInfo {
	return ProcInfo{
		PID: pid, PPID: 1, UID: 0, User: "root",
		Exe: exe, ExeOK: true, Cgroup: cgroup, StartTicks: uint64(pid) * 10,
		Cmdline:   []string{exe, "--secret=never-publish"},
		StartTime: testUnownedClock.Add(-time.Hour), CPUTicks: 100, RSS: 4096,
	}
}

type unownedHostFixture struct {
	packages   *fakePackages
	sessions   map[string]string // session id -> record content
	namespaces map[int]string    // pid -> mount namespace link
	backend    servicemgr.Backend
	openrcDirs []string
}

func (h *unownedHostFixture) runtime() unownedRuntime {
	return unownedRuntime{
		resolve: func(user string) (uint32, bool) {
			switch user {
			case "root", "0":
				return 0, true
			case "www-data", "33":
				return 33, true
			}
			return 0, false
		},
		backend:  h.backend,
		packages: h.packages,
		readFile: func(path string) ([]byte, error) {
			if content, ok := h.sessions[filepath.Base(path)]; ok {
				return []byte(content), nil
			}
			return nil, fs.ErrNotExist
		},
		readDir: func(string) ([]os.DirEntry, error) {
			root := fstest.MapFS{}
			for _, name := range h.openrcDirs {
				root[name] = &fstest.MapFile{Mode: fs.ModeDir}
			}
			return fs.ReadDir(root, ".")
		},
		readLink: func(path string) (string, error) {
			pid, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(path, "/proc/"), "/ns/mnt"))
			if err != nil {
				return "", err
			}
			if ns, ok := h.namespaces[pid]; ok {
				return ns, nil
			}
			return testHostMountNS, nil
		},
	}
}

func systemdFixture() *unownedHostFixture {
	return &unownedHostFixture{
		packages: &fakePackages{backend: pkgdb.BackendGentoo, notOwned: map[string]bool{"/opt/acme/bin/agent": true}},
		sessions: map[string]string{"3": "UID=0\nSTATE=active\n", "7": "UID=0\nSTATE=closing\n"},
		backend:  servicemgr.BackendSystemd,
	}
}

type unownedHarness struct {
	events   []Event
	snapshot checks.Result
	signaler *fakeSignaler
}

func (h *unownedHarness) watcher(t *testing.T, sampler ProcSampler, check map[string]any, rt unownedRuntime) *unownedWatcher {
	t.Helper()
	classifier, err := newUnownedClassifier(check, rt)
	if err != nil {
		t.Fatalf("newUnownedClassifier() error = %v", err)
	}
	h.signaler = &fakeSignaler{}
	emit := func(e Event) { h.events = append(h.events, e) }
	classifier.rt.sampler = sampler
	return &unownedWatcher{
		name:      "unowned",
		checkType: checks.CheckTypeUnownedProcesses,
		incidents: &pidIncidents{watch: "unowned", slot: unownedStateSlot, emit: emit},
		check:     check,
		sampler:   sampler,
		now:       func() time.Time { return testUnownedClock },
		emit:      emit,
		publish: func(watch, checkType string, result checks.Result) {
			if watch != "unowned" || checkType != checks.CheckTypeUnownedProcesses {
				t.Fatalf("published %s/%s", watch, checkType)
			}
			h.snapshot = result
		},
		classifier: classifier,
		killer:     pidKiller{watch: "unowned", signaler: h.signaler, resolve: classifier.rt.resolve, sleep: func(time.Duration) {}, emit: emit},
	}
}

// kinds lists the watch's own event kinds, without the notifier delivery
// events the dispatcher records alongside them.
func (h *unownedHarness) kinds() []string {
	var out []string
	for _, e := range h.events {
		if e.Kind == eventKindNotify {
			continue
		}
		out = append(out, e.Kind)
	}
	return out
}

func rowsOf(result checks.Result) []checks.UnownedProcess {
	rows, _ := result.Data[checks.DataKeyProcesses].([]checks.UnownedProcess)
	return rows
}

func TestUnownedWatcherListsProcessesOutsideEveryUnit(t *testing.T) {
	h := &unownedHarness{}
	stray := unownedProc(42, "/usr/bin/sleep", "0::/")
	owned := unownedProc(43, "/usr/sbin/nginx", "0::/system.slice/nginx.service")
	w := h.watcher(t, fixedSampler{infos: []ProcInfo{owned, stray}, ok: true}, map[string]any{}, systemdFixture().runtime())

	w.runCycle(context.Background())

	rows := rowsOf(h.snapshot)
	if h.snapshot.OK || len(rows) != 1 || rows[0].PID != 42 || rows[0].Reason != unownedReasonNoUnit || !rows[0].CanKill {
		t.Fatalf("snapshot = %+v rows=%+v", h.snapshot, rows)
	}
	if h.snapshot.Data[checks.DataKeyScanned] != 2 || h.snapshot.Data[checks.DataKeyViolationCount] != 1 ||
		h.snapshot.Data[checks.DataKeyUnitAttribution] != unownedAttributionAvailable || h.snapshot.Data[checks.DataKeyPackageDB] != "gentoo" {
		t.Fatalf("data = %+v", h.snapshot.Data)
	}
	if h.snapshot.Message != "unowned processes: 2 scanned, 1 unowned" {
		t.Fatalf("message = %q", h.snapshot.Message)
	}
	if findings := h.snapshot.Data[checks.DataKeyViolations]; findings != "pid 42: outside every init unit (/usr/bin/sleep)" {
		t.Fatalf("findings = %q", findings)
	}
	if kinds := h.kinds(); len(kinds) != 1 || kinds[0] != eventKindFiring || strings.Contains(h.events[0].Message, "never-publish") {
		t.Fatalf("events = %+v", h.events)
	}
}

func TestUnownedWatcherSessionAndPackageCriteria(t *testing.T) {
	h := &unownedHarness{}
	fixture := systemdFixture()
	fixture.namespaces = map[int]string{46: testOtherMountNS}
	live := unownedProc(44, "/bin/bash", "0::/user.slice/user-0.slice/session-3.scope")
	closed := unownedProc(45, "/usr/bin/sleep", "0::/user.slice/user-0.slice/session-9.scope")
	closing := unownedProc(47, "/usr/bin/sleep", "0::/user.slice/user-0.slice/session-7.scope")
	container := unownedProc(46, "/opt/acme/bin/agent", "0::/system.slice/docker-abc.scope")
	// A service's own process is owned whatever its binary: a daemon installed
	// outside the package manager, a plugin helper, sermod itself.
	servicePlugin := unownedProc(48, "/opt/acme/bin/agent", "0::/system.slice/acme.service")
	unpackagedStray := unownedProc(49, "/opt/acme/bin/agent", "0::/")
	unpackagedInSession := unownedProc(50, "/opt/acme/bin/agent", "0::/user.slice/user-0.slice/session-3.scope")
	userManagerUnit := unownedProc(51, "/opt/acme/bin/agent", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/agent.service")
	w := h.watcher(t, fixedSampler{infos: []ProcInfo{live, closed, closing, container, servicePlugin, unpackagedStray, unpackagedInSession, userManagerUnit}, ok: true}, map[string]any{}, fixture.runtime())

	w.runCycle(context.Background())

	rows := rowsOf(h.snapshot)
	got := map[int]string{}
	for _, row := range rows {
		got[row.PID] = row.Reason
	}
	want := map[int]string{
		45: unownedReasonClosedSession,
		47: unownedReasonClosedSession,
		49: unownedReasonNoUnit + unownedReasonSeparator + unownedReasonUnpackaged,
		50: unownedReasonUnpackaged,
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for pid, reason := range want {
		if got[pid] != reason {
			t.Fatalf("pid %d reason = %q, want %q (rows %+v)", pid, got[pid], reason, got)
		}
	}
	// Only the processes no unit owns are looked up: bash in the live session,
	// sleep in the closed ones and the agent outside every unit.
	if queries := fixture.packages.queries; len(queries) != 1 || len(queries[0]) != 3 {
		t.Fatalf("package lookups = %v, want one batch with the three unowned executables", queries)
	}
}

func TestUnownedClassifierSharesSessionStateOnlyWithinASample(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  string
		err    error
		closed bool
	}{
		{name: "active", state: "STATE=active\n"},
		{name: "closing", state: "STATE=closing\n", closed: true},
		{name: "missing", err: fs.ErrNotExist, closed: true},
		{name: "unreadable", err: fs.ErrPermission},
	} {
		t.Run(tc.name, func(t *testing.T) {
			samples := []ProcInfo{
				unownedProc(42, "/usr/bin/sleep", "0::/user.slice/user-0.slice/session-3.scope"),
				unownedProc(43, "/usr/bin/sleep", "0::/user.slice/user-0.slice/session-3.scope"),
			}
			rt := systemdFixture().runtime()
			rt.sampler = fixedSampler{infos: samples, ok: true}
			reads, state, readErr := 0, tc.state, tc.err
			rt.readFile = func(string) ([]byte, error) {
				reads++
				return []byte(state), readErr
			}
			classifier, err := newUnownedClassifier(nil, rt)
			if err != nil {
				t.Fatal(err)
			}
			scan := classifier.classify(t.Context(), samples, testUnownedClock)
			wantFindings := 0
			if tc.closed {
				wantFindings = len(samples)
			}
			if reads != 1 || len(scan.findings) != wantFindings {
				t.Fatalf("reads=%d findings=%d, want one session read and %d findings", reads, len(scan.findings), wantFindings)
			}
			// Re-verifying one PID must see a session that closed or reopened
			// after the preceding scan, including a previously unreadable record.
			state, readErr = "STATE=closing\n", nil
			if tc.closed {
				state = "STATE=active\n"
			}
			_, present := classifier.current(t.Context(), testUnownedClock, samples[0].PID)
			if reads != 2 || present == tc.closed {
				t.Fatalf("revalidation reads=%d present=%v, want two reads and present=%v", reads, present, !tc.closed)
			}
		})
	}
}

// A container under a cgroupfs driver sits in a path that names no unit
// (/docker/<id>, /kubepods/…, /lxc/<name>) yet is not a host stray: only a
// process confirmed in PID 1's mount namespace is judged by either criterion.
func TestUnownedWatcherNeverJudgesOtherMountNamespaces(t *testing.T) {
	h := &unownedHarness{}
	fixture := systemdFixture()
	fixture.namespaces = map[int]string{70: testOtherMountNS, 71: testOtherMountNS, 73: testOtherMountNS}
	dockerWorker := unownedProc(70, "/usr/bin/php", "0::/docker/0123abcd")
	dockerWorker.UID, dockerWorker.User = 33, "www-data"
	kubePod := unownedProc(71, "/opt/acme/bin/agent", "0::/kubepods/burstable/pod1/ctr1")
	hostInSameShape := unownedProc(72, "/opt/acme/bin/agent", "0::/lxc/payload") // host namespace: a real stray
	closedSessionInNS := unownedProc(73, "/usr/bin/sleep", "0::/user.slice/user-0.slice/session-9.scope")
	w := h.watcher(t, fixedSampler{infos: []ProcInfo{dockerWorker, kubePod, hostInSameShape, closedSessionInNS}, ok: true}, map[string]any{}, fixture.runtime())
	w.kill = unownedKillSpec([]string{"www-data", "root"}, []string{"/usr/bin/php", "/opt/acme/bin/agent"}, false)

	w.runCycle(context.Background())

	got := map[int]string{}
	for _, row := range rowsOf(h.snapshot) {
		got[row.PID] = row.Reason
	}
	want := map[int]string{
		72: unownedReasonNoUnit + unownedReasonSeparator + unownedReasonUnpackaged,
		73: unownedReasonClosedSession, // logind's verdict on a host session scope needs no namespace proof
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
	for pid, reason := range want {
		if got[pid] != reason {
			t.Fatalf("pid %d reason = %q, want %q", pid, got[pid], reason)
		}
	}
	for _, call := range h.signaler.sent {
		if call.pid == 70 || call.pid == 71 {
			t.Fatalf("signalled a container process: %+v", h.signaler.sent)
		}
	}
	if queries := fixture.packages.queries; len(queries) != 1 || len(queries[0]) != 1 || queries[0][0] != "/opt/acme/bin/agent" {
		t.Fatalf("package lookups = %v, want only the host candidate", queries)
	}
}

func TestUnownedWatcherUnreadableInitNamespaceJudgesNothing(t *testing.T) {
	h := &unownedHarness{}
	fixture := systemdFixture()
	rt := fixture.runtime()
	inner := rt.readLink
	rt.readLink = func(path string) (string, error) {
		if path == "/proc/1/ns/mnt" {
			return "", errors.New("permission denied")
		}
		return inner(path)
	}
	closedSession := unownedProc(76, "/usr/bin/sleep", "0::/user.slice/user-0.slice/session-9.scope")
	w := h.watcher(t, fixedSampler{infos: []ProcInfo{unownedProc(74, "/opt/acme/bin/agent", "0::/"), closedSession}, ok: true}, map[string]any{}, rt)

	w.runCycle(context.Background())

	// The unit and package criteria are off; logind's closed-session verdict stands.
	rows := rowsOf(h.snapshot)
	if h.snapshot.OK || len(rows) != 1 || rows[0].PID != 76 || rows[0].Reason != unownedReasonClosedSession {
		t.Fatalf("snapshot = %+v", h.snapshot)
	}
	if !strings.Contains(h.snapshot.Message, unownedCaveatInitNamespace) || strings.Contains(h.snapshot.Message, "unreadable mount namespace") {
		t.Fatalf("message = %q", h.snapshot.Message)
	}
}

// An unprivileged daemon cannot read other users' namespace links: those
// processes are not judged, and the scan says how many.
func TestUnownedWatcherCountsUnreadableProcessNamespaces(t *testing.T) {
	h := &unownedHarness{}
	rt := systemdFixture().runtime()
	inner := rt.readLink
	rt.readLink = func(path string) (string, error) {
		if path == "/proc/77/ns/mnt" {
			return "", errors.New("permission denied")
		}
		return inner(path)
	}
	hidden := unownedProc(77, "/opt/acme/bin/agent", "0::/")
	visible := unownedProc(78, "/opt/acme/bin/agent", "0::/")
	w := h.watcher(t, fixedSampler{infos: []ProcInfo{hidden, visible}, ok: true}, map[string]any{}, rt)

	w.runCycle(context.Background())

	rows := rowsOf(h.snapshot)
	if len(rows) != 1 || rows[0].PID != 78 {
		t.Fatalf("rows = %+v, want only pid 78", rows)
	}
	if !strings.Contains(h.snapshot.Message, "(1 with unreadable mount namespace") {
		t.Fatalf("message = %q", h.snapshot.Message)
	}
}

func TestUnownedWatcherReportsUnresolvedUsers(t *testing.T) {
	stray := unownedProc(75, "/opt/acme/bin/agent", "0::/")

	// One name resolves, one does not: the scan runs over the resolved account
	// and says which one it is blind to.
	h := &unownedHarness{}
	w := h.watcher(t, fixedSampler{infos: []ProcInfo{stray}, ok: true}, map[string]any{checks.CheckKeyUsers: []any{"root", "wwwdata"}}, systemdFixture().runtime())
	w.runCycle(context.Background())
	if h.snapshot.OK || len(rowsOf(h.snapshot)) != 1 {
		t.Fatalf("partial: snapshot = %+v", h.snapshot)
	}
	if !strings.Contains(h.snapshot.Message, unownedCaveatUnresolved+"wwwdata") {
		t.Fatalf("partial: message = %q", h.snapshot.Message)
	}

	// Nothing resolves: every process is excluded, and a scan of nothing is a
	// failure to look, never a clean host.
	h2 := &unownedHarness{}
	w2 := h2.watcher(t, fixedSampler{infos: []ProcInfo{stray}, ok: true}, map[string]any{checks.CheckKeyUsers: []any{"wwwdata", "nobody2"}}, systemdFixture().runtime())
	w2.runCycle(context.Background())
	if h2.snapshot.OK || len(rowsOf(h2.snapshot)) != 0 || h2.snapshot.Data[checks.DataKeyScanned] != 0 {
		t.Fatalf("unresolved: snapshot = %+v", h2.snapshot)
	}
	if !strings.Contains(h2.snapshot.Message, unownedCaveatUnresolved+"wwwdata, nobody2)") {
		t.Fatalf("unresolved: message = %q", h2.snapshot.Message)
	}
	if kinds := h2.kinds(); len(kinds) != 0 {
		t.Fatalf("unresolved: no incident may open, got events %v", kinds)
	}
}

func TestUnownedWatcherExclusions(t *testing.T) {
	h := &unownedHarness{}
	young := unownedProc(50, "/usr/bin/sleep", "0::/")
	young.StartTime = testUnownedClock.Add(-time.Minute)
	noStart := unownedProc(51, "/usr/bin/sleep", "0::/")
	noStart.StartTime = time.Time{}
	zombie := unownedProc(52, "/usr/bin/sleep", "0::/")
	zombie.State = process.ProcStateZombie
	kthread := unownedProc(2, "", "0::/")
	kthread.ExeOK, kthread.Cmdline = false, nil
	ignored := unownedProc(53, "/usr/bin/SCREEN", "0::/")
	ignoredByCmd := unownedProc(54, "/opt/acme/bin/agent", "0::/")
	ignoredByCmd.Cmdline = []string{"/opt/acme/bin/agent", "--daemon"}
	notIgnoredByCmd := unownedProc(55, "/opt/acme/bin/agent", "0::/")
	notIgnoredByCmd.Cmdline = []string{"/opt/acme/bin/agent", "--other"}
	otherUser := unownedProc(56, "/usr/bin/sleep", "0::/")
	otherUser.UID, otherUser.User = 1000, "guest"
	listed := unownedProc(57, "/usr/bin/sleep", "0::/")
	unreadable := unownedProc(58, "", "0::/")
	unreadable.ExeOK = false
	replaced := unownedProc(59, "", "0::/")
	replaced.ExeOK, replaced.ExePrev = false, "/opt/acme/bin/agent"
	check := map[string]any{
		checks.CheckKeyMinAge: "5m",
		checks.CheckKeyUsers:  []any{"root"},
		checks.CheckKeyIgnore: map[string]any{
			"screen": map[string]any{checks.CheckKeyExe: "/usr/bin/SCREEN"},
			"agent":  map[string]any{checks.CheckKeyExe: "/opt/acme/bin/agent", process.SelectorKeyCmd: "^/opt/acme/bin/agent --daemon$"},
		},
	}
	w := h.watcher(t, fixedSampler{infos: []ProcInfo{young, noStart, zombie, kthread, ignored, ignoredByCmd, notIgnoredByCmd, otherUser, listed, unreadable, replaced}, ok: true}, check, systemdFixture().runtime())

	w.runCycle(context.Background())

	rows := rowsOf(h.snapshot)
	var pids []int
	for _, row := range rows {
		pids = append(pids, row.PID)
	}
	if len(pids) != 4 || pids[0] != 55 || pids[1] != 57 || pids[2] != 58 || pids[3] != 59 {
		t.Fatalf("listed pids = %v, want [55 57 58 59]", pids)
	}
	if rows[2].CanKill || rows[2].KillReason != unownedKillReasonUnresolvedExe || rows[3].CanKill || rows[3].KillReason != unownedKillReasonReplacedExe {
		t.Fatalf("kill flags = %+v %+v", rows[2], rows[3])
	}
	if rows[3].Reason != unownedReasonNoUnit+unownedReasonSeparator+unownedReasonUnpackaged || rows[3].ExePrevious != "/opt/acme/bin/agent" {
		t.Fatalf("replaced executable row = %+v", rows[3])
	}
}

func TestUnownedWatcherReportsUnavailableAttribution(t *testing.T) {
	h := &unownedHarness{}
	fixture := systemdFixture()
	fixture.backend = servicemgr.BackendOpenRC
	fixture.packages = &fakePackages{backend: pkgdb.BackendNone}
	w := h.watcher(t, fixedSampler{infos: []ProcInfo{unownedProc(60, "/opt/acme/bin/agent", "0::/")}, ok: true}, map[string]any{}, fixture.runtime())

	w.runCycle(context.Background())

	if !h.snapshot.OK || len(rowsOf(h.snapshot)) != 0 {
		t.Fatalf("snapshot = %+v", h.snapshot)
	}
	if !strings.Contains(h.snapshot.Message, "init unit attribution unavailable") || !strings.Contains(h.snapshot.Message, "no package database") {
		t.Fatalf("message = %q", h.snapshot.Message)
	}
	if h.snapshot.Data[checks.DataKeyUnitAttribution] != unownedAttributionUnavailable || h.snapshot.Data[checks.DataKeyPackageDB] != "none" {
		t.Fatalf("data = %+v", h.snapshot.Data)
	}

	// OpenRC with per-service groups attributes again.
	fixture.openrcDirs = []string{"openrc.sshd"}
	h2 := &unownedHarness{}
	w2 := h2.watcher(t, fixedSampler{infos: []ProcInfo{unownedProc(60, "/opt/acme/bin/agent", "0::/")}, ok: true}, map[string]any{}, fixture.runtime())
	w2.runCycle(context.Background())
	if h2.snapshot.OK || len(rowsOf(h2.snapshot)) != 1 {
		t.Fatalf("openrc unit groups: snapshot = %+v", h2.snapshot)
	}
}

func TestUnownedWatcherFiresOncePerIncarnationAndRecovers(t *testing.T) {
	h := &unownedHarness{}
	stray := unownedProc(42, "/usr/bin/sleep", "0::/")
	recycled := stray
	recycled.StartTicks = 999
	sampler := &fakeProcSampler{cycles: [][]ProcInfo{{stray}, {stray}, {recycled}, {}}}
	n := &fakeNotifier{name: "ops"}
	w := h.watcher(t, sampler, map[string]any{}, systemdFixture().runtime())
	w.notifiers = []notify.Notifier{n}

	for range 4 {
		w.runCycle(context.Background())
	}

	kinds := h.kinds()
	if len(kinds) != 3 || kinds[0] != eventKindFiring || kinds[1] != eventKindFiring || kinds[2] != eventKindRecovered {
		t.Fatalf("events = %v", kinds)
	}
	if len(n.msgs) != 3 || !strings.HasPrefix(n.msgs[2].Body, recoveredMessagePrefix) {
		t.Fatalf("notifications = %+v", n.msgs)
	}
	if rows := rowsOf(h.snapshot); len(rows) != 0 || !h.snapshot.OK {
		t.Fatalf("final snapshot = %+v", h.snapshot)
	}
}

func TestUnownedWatcherReportsCPUFromSecondCycle(t *testing.T) {
	h := &unownedHarness{}
	first := unownedProc(42, "/usr/bin/sleep", "0::/")
	second := first
	second.CPUTicks = first.CPUTicks + 50
	clock := testUnownedClock
	w := h.watcher(t, &fakeProcSampler{cycles: [][]ProcInfo{{first}, {second}}}, map[string]any{}, systemdFixture().runtime())
	w.now = func() time.Time { return clock }

	w.runCycle(context.Background())
	if rows := rowsOf(h.snapshot); rows[0].HasCPU {
		t.Fatalf("first cycle must not report a CPU rate: %+v", rows[0])
	}
	clock = clock.Add(10 * time.Second)
	w.runCycle(context.Background())
	if rows := rowsOf(h.snapshot); !rows[0].HasCPU || rows[0].CPU <= 0 || rows[0].RSS != 4096 {
		t.Fatalf("second cycle row = %+v", rows[0])
	}
}

func TestUnownedWatcherSampleFailurePublishesFailure(t *testing.T) {
	h := &unownedHarness{}
	w := h.watcher(t, fixedSampler{ok: false}, map[string]any{}, systemdFixture().runtime())
	w.runCycle(context.Background())
	if h.snapshot.OK || !strings.Contains(h.snapshot.Message, "sample unavailable") || len(h.events) != 0 {
		t.Fatalf("snapshot = %+v events = %+v", h.snapshot, h.events)
	}
}

func unownedKillSpec(users, exes []string, escalate bool) *killSpec {
	return &killSpec{signal: syscall.SIGTERM, escalate: escalate, termTimeout: time.Second, killTimeout: time.Second, selector: process.NewKillSelector(users, exes)}
}

func TestUnownedWatcherKillsOnlyAuthorizedFindings(t *testing.T) {
	h := &unownedHarness{}
	authorized := unownedProc(42, "/usr/bin/sleep", "0::/")
	other := unownedProc(43, "/usr/bin/python3", "0::/")
	w := h.watcher(t, fixedSampler{infos: []ProcInfo{authorized, other}, ok: true}, map[string]any{}, systemdFixture().runtime())
	w.kill = unownedKillSpec([]string{"root"}, []string{"/usr/bin/sleep"}, false)

	w.runCycle(context.Background())

	if len(h.signaler.sent) != 1 || h.signaler.sent[0] != (sigCall{42, syscall.SIGTERM}) {
		t.Fatalf("signals = %v, want one SIGTERM to pid 42", h.signaler.sent)
	}
	kinds := h.kinds()
	if len(kinds) != 3 || kinds[0] != eventKindFiring || kinds[1] != eventKindKill || kinds[2] != eventKindFiring {
		t.Fatalf("events = %v", kinds)
	}
	// Steady state: the same incarnation is never signalled twice.
	w.runCycle(context.Background())
	if len(h.signaler.sent) != 1 {
		t.Fatalf("re-signalled a steady PID: %v", h.signaler.sent)
	}
}

func TestUnownedWatcherNeverSignalsReplacedOrUnresolvedExecutable(t *testing.T) {
	h := &unownedHarness{}
	replaced := unownedProc(42, "", "0::/")
	replaced.ExeOK, replaced.ExePrev = false, "/usr/bin/sleep"
	unresolved := unownedProc(43, "", "0::/")
	unresolved.ExeOK = false
	w := h.watcher(t, fixedSampler{infos: []ProcInfo{replaced, unresolved}, ok: true}, map[string]any{}, systemdFixture().runtime())
	w.kill = unownedKillSpec([]string{"root"}, []string{"/usr/bin/sleep"}, true)

	w.runCycle(context.Background())

	if len(h.signaler.sent) != 0 {
		t.Fatalf("signalled a process without an exact executable: %v", h.signaler.sent)
	}
	if rows := rowsOf(h.snapshot); len(rows) != 2 || rows[0].CanKill || rows[1].CanKill {
		t.Fatalf("rows = %+v, want both listed and neither killable", rows)
	}
	for _, kind := range h.kinds() {
		if kind != eventKindFiring {
			t.Fatalf("unexpected event kind %q in %v", kind, h.kinds())
		}
	}
}

func TestUnownedWatcherKillEscalatesWhileStillUnowned(t *testing.T) {
	h := &unownedHarness{}
	stray := unownedProc(42, "/usr/bin/sleep", "0::/")
	adopted := stray
	adopted.Cgroup = "0::/system.slice/sleep.service"
	// Cycle sample, post-TERM re-sample (still unowned), post-KILL re-sample (gone).
	w := h.watcher(t, &fakeProcSampler{cycles: [][]ProcInfo{{stray}, {stray}, {}}}, map[string]any{}, systemdFixture().runtime())
	w.kill = unownedKillSpec([]string{"root"}, []string{"/usr/bin/sleep"}, true)
	w.runCycle(context.Background())
	if len(h.signaler.sent) != 2 || h.signaler.sent[1] != (sigCall{42, syscall.SIGKILL}) {
		t.Fatalf("signals = %v, want TERM then KILL", h.signaler.sent)
	}

	// A process that became owned during the grace period is never escalated on.
	h2 := &unownedHarness{}
	w2 := h2.watcher(t, &fakeProcSampler{cycles: [][]ProcInfo{{stray}, {adopted}}}, map[string]any{}, systemdFixture().runtime())
	w2.kill = unownedKillSpec([]string{"root"}, []string{"/usr/bin/sleep"}, true)
	w2.runCycle(context.Background())
	if len(h2.signaler.sent) != 1 || h2.signaler.sent[0].sig != syscall.SIGTERM {
		t.Fatalf("signals = %v, want only SIGTERM", h2.signaler.sent)
	}
}

// Many findings in one cycle: each gets its first signal as it fires, and the
// escalations are finished together — one TERM grace and one KILL grace for
// the whole batch, not one pair per finding.
func TestUnownedWatcherEscalatesACycleTogether(t *testing.T) {
	h := &unownedHarness{}
	a := unownedProc(42, "/usr/bin/sleep", "0::/")
	b := unownedProc(43, "/usr/bin/sleep", "0::/")
	// Cycle sample, two post-TERM re-samples (both still unowned), two post-KILL re-samples (gone).
	w := h.watcher(t, &fakeProcSampler{cycles: [][]ProcInfo{{a, b}, {a, b}, {a, b}, {}, {}}}, map[string]any{}, systemdFixture().runtime())
	w.kill = unownedKillSpec([]string{"root"}, []string{"/usr/bin/sleep"}, true)
	var waits []time.Duration
	w.killer.sleep = func(d time.Duration) { waits = append(waits, d) }

	w.runCycle(context.Background())

	want := []sigCall{{42, syscall.SIGTERM}, {43, syscall.SIGTERM}, {42, syscall.SIGKILL}, {43, syscall.SIGKILL}}
	if !slices.Equal(h.signaler.sent, want) {
		t.Fatalf("signals = %v, want %v", h.signaler.sent, want)
	}
	if len(waits) != 2 || waits[0] != w.kill.termTimeout || waits[1] != w.kill.killTimeout {
		t.Fatalf("waits = %v, want one TERM grace then one KILL grace", waits)
	}
	kinds := h.kinds()
	wantKinds := []string{eventKindFiring, eventKindKill, eventKindFiring, eventKindKill, eventKindKill, eventKindKill}
	if !slices.Equal(kinds, wantKinds) {
		t.Fatalf("events = %v, want %v", kinds, wantKinds)
	}
	if w.pending != nil {
		t.Fatalf("pending escalations not cleared: %d", len(w.pending))
	}
}

// Re-verifying one PID samples that PID alone and judges it by the same rules.
func TestUnownedClassifierCurrentSamplesOnePID(t *testing.T) {
	stray := unownedProc(42, "/opt/acme/bin/agent", "0::/")
	owned := unownedProc(43, "/opt/acme/bin/agent", "0::/system.slice/acme.service")
	sampler := &matchRecordingSampler{infos: []ProcInfo{stray, owned}}
	rt := systemdFixture().runtime()
	rt.sampler = sampler
	classifier, err := newUnownedClassifier(map[string]any{}, rt)
	if err != nil {
		t.Fatal(err)
	}

	if info, ok := classifier.current(context.Background(), testUnownedClock, 42); !ok || info.PID != 42 {
		t.Fatalf("current(42) = %+v, %v", info, ok)
	}
	if _, ok := classifier.current(context.Background(), testUnownedClock, 43); ok {
		t.Fatal("current(43) listed a service member")
	}
	if _, ok := classifier.current(context.Background(), testUnownedClock, 44); ok {
		t.Fatal("current(44) listed an absent pid")
	}
	if len(sampler.matches) != 3 || sampler.matches[0] != (ProcMatch{PID: 42}) || sampler.matches[2] != (ProcMatch{PID: 44}) {
		t.Fatalf("sampled with %+v, want one PID selector per call", sampler.matches)
	}
}

type matchRecordingSampler struct {
	infos   []ProcInfo
	matches []ProcMatch
}

func (s *matchRecordingSampler) Sample(m ProcMatch) ([]ProcInfo, bool) {
	s.matches = append(s.matches, m)
	var out []ProcInfo
	for _, info := range s.infos {
		if m.PID == 0 || info.PID == m.PID {
			out = append(out, info)
		}
	}
	return out, true
}

// Without logind's session directory a missing record proves nothing, so no
// session is called closed and the scan says why.
func TestUnownedWatcherUnreadableSessionDirectoryClosesNothing(t *testing.T) {
	h := &unownedHarness{}
	rt := systemdFixture().runtime()
	rt.readDir = func(string) ([]os.DirEntry, error) { return nil, fs.ErrNotExist }
	closedSession := unownedProc(79, "/usr/bin/sleep", "0::/user.slice/user-0.slice/session-9.scope")
	stray := unownedProc(80, "/opt/acme/bin/agent", "0::/")
	w := h.watcher(t, fixedSampler{infos: []ProcInfo{closedSession, stray}, ok: true}, map[string]any{}, rt)

	w.runCycle(context.Background())

	rows := rowsOf(h.snapshot)
	if len(rows) != 1 || rows[0].PID != 80 {
		t.Fatalf("rows = %+v, want only the root-group stray", rows)
	}
	if !strings.Contains(h.snapshot.Message, unownedCaveatNoSessions) {
		t.Fatalf("message = %q", h.snapshot.Message)
	}
}

func TestUnownedWatcherDryRunSuppressesKillAndNotify(t *testing.T) {
	h := &unownedHarness{}
	n := &fakeNotifier{name: "ops"}
	w := h.watcher(t, fixedSampler{infos: []ProcInfo{unownedProc(42, "/usr/bin/sleep", "0::/")}, ok: true}, map[string]any{}, systemdFixture().runtime())
	w.kill = unownedKillSpec([]string{"root"}, []string{"/usr/bin/sleep"}, false)
	w.notifiers = []notify.Notifier{n}
	w.dryRun = true

	w.runCycle(context.Background())

	if len(h.signaler.sent) != 0 || len(n.msgs) != 0 {
		t.Fatalf("dry run signalled %v or notified %d", h.signaler.sent, len(n.msgs))
	}
	var dry bool
	for _, e := range h.events {
		if e.Kind == eventKindDryRun && strings.Contains(e.Message, "kill") && strings.Contains(e.Message, "notify") {
			dry = true
		}
	}
	if !dry {
		t.Fatalf("want a dry-run event naming kill and notify, got %+v", h.events)
	}
}

func TestBuildUnownedProcessesWatchRefusals(t *testing.T) {
	base := func(then map[string]any) map[string]any {
		entry := map[string]any{"check": map[string]any{"type": checks.CheckTypeUnownedProcesses}}
		if then != nil {
			entry["then"] = then
		}
		return entry
	}
	for _, tc := range []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{"hook", base(map[string]any{"hook": map[string]any{"command": []any{"/bin/false"}}}), "then.hook is not valid"},
		{"kill without selector", base(map[string]any{"kill": map[string]any{"signal": "TERM"}}), "kill_only_if"},
		{"half selector", base(map[string]any{"kill": map[string]any{"kill_only_if": map[string]any{"users": []any{"root"}}}}), "exe_any"},
		{"bad min_age", map[string]any{"check": map[string]any{"type": checks.CheckTypeUnownedProcesses, "min_age": "0s"}}, "min_age"},
		{"bad ignore", map[string]any{"check": map[string]any{"type": checks.CheckTypeUnownedProcesses, "ignore": map[string]any{"x": map[string]any{"exe": "relative"}}}}, "ignore.x.exe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			watches, warnings := BuildWatches(cfgWithWatches(map[string]any{"unowned": tc.entry}), Deps{PackageIndex: &fakePackages{}}, time.Minute)
			if len(watches) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], tc.want) {
				t.Fatalf("BuildWatches() = watches:%d warnings:%v, want %q", len(watches), warnings, tc.want)
			}
		})
	}
	valid := base(map[string]any{"notify": []any{"none"}, "kill": map[string]any{"escalate": true, "kill_only_if": map[string]any{"users": []any{"root"}, "exe_any": []any{"/usr/bin/sleep"}}}})
	watches, warnings := BuildWatches(cfgWithWatches(map[string]any{"unowned": valid}), Deps{PackageIndex: &fakePackages{}}, time.Minute)
	if len(watches) != 1 || len(warnings) != 0 || watches[0].CheckType != checks.CheckTypeUnownedProcesses || watches[0].Severity != severity.Warning {
		t.Fatalf("BuildWatches() = %d watches severity=%q warnings:%v, want one advisory watch", len(watches), watches[0].Severity, warnings)
	}
	graded := base(nil)
	graded["severity"] = "error"
	if watches, _ := BuildWatches(cfgWithWatches(map[string]any{"unowned": graded}), Deps{PackageIndex: &fakePackages{}}, time.Minute); len(watches) != 1 || watches[0].Severity != severity.Error {
		t.Fatalf("declared severity not honoured: %+v", watches)
	}
	if got := unsupportedServiceWatchType(valid); got == "" {
		t.Fatal("unowned_processes must stay host-scoped")
	}
}
