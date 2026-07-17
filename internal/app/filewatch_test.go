package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/notify"
)

// fileWatchHarness records each hook firing (its env) and the emitted events.
type fileWatchHarness struct {
	fired  []map[string]string
	events []Event
}

func (h *fileWatchHarness) watcher(path string, recursive bool, cond fileCond) *fileWatcher {
	return &fileWatcher{
		name:      "fw",
		paths:     []string{path},
		recursive: recursive,
		cond:      cond,
		hook:      HookSpec{Command: []string{"/bin/true"}},
		runner: HookRunnerFunc(func(_ context.Context, _ []string, env map[string]string, _ time.Duration) error {
			h.fired = append(h.fired, env)
			return nil
		}),
		emit: func(e Event) { h.events = append(h.events, e) },
	}
}

func writeSize(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFileWatchFirstCycleSilent(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.txt")
	writeSize(t, f, 10)
	h := &fileWatchHarness{}
	w := h.watcher(f, false, fileCond{sizeChange: true, permChange: true})
	w.runCycle(context.Background())
	if len(h.fired) != 0 {
		t.Fatalf("first cycle must adopt the baseline silently, fired %d hooks", len(h.fired))
	}
}

func TestFileWatchOlderThanFiresPerPathAndRearms(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.txt")
	freshPath := filepath.Join(dir, "fresh.txt")
	writeSize(t, oldPath, 1)
	writeSize(t, freshPath, 1)
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(oldPath, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(freshPath, now.Add(-30*time.Minute), now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}

	h := &fileWatchHarness{}
	w := h.watcher(oldPath, false, fileCond{olderThan: time.Hour})
	w.paths = []string{oldPath, freshPath}
	w.now = func() time.Time { return now }

	w.runCycle(context.Background())
	if len(h.fired) != 1 || h.fired[0][sermoEnvPath] != oldPath || h.fired[0][sermoEnvChange] != fileChangeOlderThan {
		t.Fatalf("first stale path fire = %v, want only %s", h.fired, oldPath)
	}
	if len(h.events) != 1 || h.events[0].Message != oldPath+" was modified at 2026-07-12T10:00:00Z and is older than 1h" {
		t.Fatalf("stale path event = %+v", h.events)
	}
	w.runCycle(context.Background())
	if len(h.fired) != 1 {
		t.Fatalf("stale path repeated without rearming: %v", h.fired)
	}

	if err := os.Chtimes(oldPath, now, now); err != nil {
		t.Fatal(err)
	}
	w.runCycle(context.Background())
	if len(h.fired) != 1 {
		t.Fatalf("fresh path must only rearm, fired=%v", h.fired)
	}
	if err := os.Chtimes(oldPath, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	w.runCycle(context.Background())
	if len(h.fired) != 2 || h.fired[1][sermoEnvAgeSeconds] != "7200" {
		t.Fatalf("re-armed stale path fire = %v", h.fired)
	}
}

func TestFileWatchOlderThanAggregatesOneEventPerCycle(t *testing.T) {
	// A recursive watch over a directory of stale files used to burst one
	// event per file with the same timestamp (GeoIP x12 in the fleet). The
	// hooks keep their per-file contract; the visible event is one per cycle.
	dir := t.TempDir()
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	stale := make([]string, 0, 7)
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		p := filepath.Join(dir, name+".mmdb")
		writeSize(t, p, 1)
		if err := os.Chtimes(p, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
			t.Fatal(err)
		}
		stale = append(stale, p)
	}
	h := &fileWatchHarness{}
	w := h.watcher(dir, true, fileCond{olderThan: time.Hour})
	w.now = func() time.Time { return now }

	w.runCycle(context.Background())

	if len(h.fired) != len(stale) {
		t.Fatalf("hooks must keep firing once per stale file, fired %d, want %d", len(h.fired), len(stale))
	}
	hookEvents := 0
	var aggregated []Event
	for _, e := range h.events {
		switch e.Kind {
		case eventKindHook:
			hookEvents++
		default:
			aggregated = append(aggregated, e)
		}
	}
	if hookEvents != len(stale) {
		t.Fatalf("hook events = %d, want one per stale file (%d): %+v", hookEvents, len(stale), h.events)
	}
	if len(aggregated) != 0 {
		t.Fatalf("unexpected extra events: %+v", aggregated)
	}

	// Re-run: nothing re-fires until the files are touched (rearm contract).
	w.runCycle(context.Background())
	if len(h.fired) != len(stale) {
		t.Fatalf("stale files repeated without rearming: %d", len(h.fired))
	}
}

func TestFileWatchOlderThanAggregatedDryRunEvent(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		p := filepath.Join(dir, name+".mmdb")
		writeSize(t, p, 1)
		if err := os.Chtimes(p, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	h := &fileWatchHarness{}
	w := h.watcher(dir, true, fileCond{olderThan: time.Hour})
	w.dryRun = true
	w.now = func() time.Time { return now }

	w.runCycle(context.Background())

	if len(h.fired) != 0 {
		t.Fatalf("dry-run must not execute hooks, fired %d", len(h.fired))
	}
	if len(h.events) != 1 || h.events[0].Kind != eventKindDryRun {
		t.Fatalf("dry-run must emit one aggregated event per cycle, got %+v", h.events)
	}
	msg := h.events[0].Message
	if !strings.Contains(msg, "7 files older than 1h") || !strings.Contains(msg, "(+2 more)") {
		t.Fatalf("aggregated message must carry the count and bounded list, got %q", msg)
	}
}

func TestFileWatchSummaryUsesObservedAgeAndFileCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.txt")
	writeSize(t, path, 1)
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	h := &fileWatchHarness{}
	w := h.watcher(path, false, fileCond{olderThan: time.Hour})
	w.summary = "GeoIP ${value} is older than ${older_than} in ${number_files} files"
	w.check = map[string]any{checks.CheckKeyOlderThan: "1h"}
	w.now = func() time.Time { return now }

	w.runCycle(context.Background())

	const want = "GeoIP 2h is older than 1h in 1 files"
	if len(h.fired) != 1 || h.fired[0][sermoEnvMessage] != want {
		t.Fatalf("hook env = %v, want summary %q", h.fired, want)
	}
	if len(h.events) != 1 || h.events[0].Message != want {
		t.Fatalf("events = %+v, want summary %q", h.events, want)
	}
}

func TestFileWatchOlderThanFiresAfterObserveOnlyCycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.txt")
	writeSize(t, path, 1)
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	h := &fileWatchHarness{}
	w := h.watcher(path, false, fileCond{olderThan: time.Hour})
	w.now = func() time.Time { return now }

	w.runCycle(withObserveOnly(context.Background(), true))
	if len(h.fired) != 0 {
		t.Fatalf("observe-only cycle fired=%v", h.fired)
	}
	w.runCycle(context.Background())
	if len(h.fired) != 1 || h.fired[0][sermoEnvChange] != fileChangeOlderThan {
		t.Fatalf("stale path did not fire after observe-only cycle: %v", h.fired)
	}
}

func TestFileWatchPublishesSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeSize(t, filepath.Join(dir, "a.txt"), 10)
	h := &fileWatchHarness{}
	w := h.watcher(dir, true, fileCond{sizeChange: true})
	var got checks.Result
	w.publish = func(watch, checkType string, res checks.Result) {
		if watch != "fw" || checkType != checks.CheckTypeFile {
			t.Fatalf("publish target = %s/%s", watch, checkType)
		}
		got = res
	}

	w.runCycle(context.Background())

	if !got.OK || got.Data[checks.DataKeyPath] != dir {
		t.Fatalf("published snapshot = %+v", got)
	}
	if got.Data[checks.DataKeyKind] != checks.FileKindDirectory {
		t.Fatalf("kind = %v, want directory", got.Data[checks.DataKeyKind])
	}
	if got.Data[watchReadingFieldEntries] != 1 {
		t.Fatalf("entries = %v, want 1", got.Data[watchReadingFieldEntries])
	}
}

func TestFileWatchRecursiveSkipsHiddenEntriesByDefault(t *testing.T) {
	root := t.TempDir()
	for path, size := range map[string]int{
		"visible.txt":       1,
		".hidden.txt":       1,
		".cache/nested.txt": 1,
	} {
		fullPath := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		writeSize(t, fullPath, size)
	}

	w := (&fileWatchHarness{}).watcher(root, true, fileCond{sizeChange: true})
	if got := fileWatchNumberFiles(w.scan(time.Now())); got != 1 {
		t.Fatalf("default recursive files = %d, want 1", got)
	}
	w.includeHidden = true
	if got := fileWatchNumberFiles(w.scan(time.Now())); got != 3 {
		t.Fatalf("include_hidden recursive files = %d, want 3", got)
	}

	hiddenRoot := filepath.Join(root, ".explicit")
	if err := os.Mkdir(hiddenRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSize(t, filepath.Join(hiddenRoot, "visible.txt"), 1)
	w.paths = []string{hiddenRoot}
	w.includeHidden = false
	if got := fileWatchNumberFiles(w.scan(time.Now())); got != 1 {
		t.Fatalf("explicit hidden root files = %d, want 1", got)
	}
}

func TestFileWatchSnapshotUsesReadableAge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	writeSize(t, path, 10)
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, now.Add(-25*time.Hour), now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}

	w := (&fileWatchHarness{}).watcher(path, false, fileCond{olderThan: time.Hour})
	w.now = func() time.Time { return now }
	var got checks.Result
	w.publish = func(_, _ string, res checks.Result) { got = res }

	w.runCycle(context.Background())

	if age := got.Data[checks.DataKeyAge]; age != "1d1h" {
		t.Fatalf("snapshot age = %v, want 1d1h", age)
	}
}

func TestFileWatchPublishesMissingSnapshot(t *testing.T) {
	h := &fileWatchHarness{}
	w := h.watcher(filepath.Join(t.TempDir(), "missing"), false, fileCond{onDelete: true})
	var got checks.Result
	w.publish = func(_, _ string, res checks.Result) { got = res }

	w.runCycle(context.Background())

	if got.OK || got.Data[checks.DataKeyPaths] == nil || got.Message == "" {
		t.Fatalf("missing snapshot = %+v", got)
	}
}

func TestFileWatchSizeChange(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.txt")
	writeSize(t, f, 10)
	h := &fileWatchHarness{}
	w := h.watcher(f, false, fileCond{sizeChange: true})

	w.runCycle(context.Background()) // adopt
	writeSize(t, f, 25)
	w.runCycle(context.Background()) // size changed -> 1 fire

	if len(h.fired) != 1 {
		t.Fatalf("size change fired %d times, want 1", len(h.fired))
	}
	env := h.fired[0]
	if env["SERMO_CHANGE"] != "size" || env["SERMO_NEW"] != "25" || env["SERMO_PATH"] != f {
		t.Fatalf("unexpected hook env: %v", env)
	}
	if len(h.events) != 1 || h.events[0].Kind != eventKindHook {
		t.Fatalf("want one hook event, got %+v", h.events)
	}
}

func TestFileWatchDryRunSkipsHookAndNotify(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.txt")
	writeSize(t, f, 10)
	h := &fileWatchHarness{}
	w := h.watcher(f, false, fileCond{sizeChange: true})
	n := &fakeNotifier{name: "ops"}
	w.notifiers = []notify.Notifier{n}
	w.dryRun = true

	w.runCycle(context.Background()) // adopt
	writeSize(t, f, 25)
	w.runCycle(context.Background()) // would fire

	if len(h.fired) != 0 {
		t.Fatalf("dry-run must not execute hook, fired=%v", h.fired)
	}
	if len(n.msgs) != 0 {
		t.Fatalf("dry-run must not notify, got %d messages", len(n.msgs))
	}
	if len(h.events) != 1 || h.events[0].Kind != eventKindDryRun {
		t.Fatalf("expected one dry-run event, got %+v", h.events)
	}
}

func TestFileWatchSizeThresholdIsEdgeTriggered(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.txt")
	writeSize(t, f, 10)
	h := &fileWatchHarness{}
	w := h.watcher(f, false, fileCond{sizeOp: ">", sizeValue: 100})

	w.runCycle(context.Background()) // adopt, not breached
	writeSize(t, f, 150)
	w.runCycle(context.Background()) // crosses -> fire
	w.runCycle(context.Background()) // still above -> no fire
	if len(h.fired) != 1 {
		t.Fatalf("threshold fired %d times after one crossing, want 1", len(h.fired))
	}

	writeSize(t, f, 50)
	w.runCycle(context.Background()) // drops below -> rearm, no fire
	writeSize(t, f, 200)
	w.runCycle(context.Background()) // crosses again -> fire
	if len(h.fired) != 2 {
		t.Fatalf("threshold fired %d times total, want 2 (re-armed after dropping)", len(h.fired))
	}
	if h.fired[1]["SERMO_CHANGE"] != "size_threshold" || h.fired[1]["SERMO_OP"] != ">" {
		t.Fatalf("unexpected threshold env: %v", h.fired[1])
	}
}

func TestFileWatchObserveOnlySkipsFire(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.txt")
	writeSize(t, f, 10)
	h := &fileWatchHarness{}
	w := h.watcher(f, false, fileCond{sizeChange: true})

	w.runCycle(withObserveOnly(context.Background(), true)) // adopt baseline only
	if len(h.fired) != 0 {
		t.Fatalf("observe-only adopt fired %d times, want 0", len(h.fired))
	}
	writeSize(t, f, 99)
	w.runCycle(context.Background()) // settled: size change fires
	if len(h.fired) != 1 {
		t.Fatalf("settled cycle fired %d times, want 1", len(h.fired))
	}
}

func TestFileWatchPermissions(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.txt")
	writeSize(t, f, 1)
	h := &fileWatchHarness{}
	w := h.watcher(f, false, fileCond{permChange: true})

	w.runCycle(context.Background()) // adopt (0644)
	if err := os.Chmod(f, 0o600); err != nil {
		t.Fatal(err)
	}
	w.runCycle(context.Background())

	if len(h.fired) != 1 {
		t.Fatalf("permission change fired %d times, want 1", len(h.fired))
	}
	if got := h.fired[0]; got["SERMO_CHANGE"] != "permissions" || got["SERMO_NEW"] != "0600" {
		t.Fatalf("unexpected perms env: %v", got)
	}
}

func TestFileWatchOwnerChange(t *testing.T) {
	// Changing owner needs privileges, so simulate it: adopt a baseline with a
	// bogus uid, then a cycle reads the real uid and detects the difference.
	f := filepath.Join(t.TempDir(), "a.txt")
	writeSize(t, f, 1)
	h := &fileWatchHarness{}
	w := h.watcher(f, false, fileCond{ownerChange: true})
	w.baseline = map[string]fileState{f: {size: 1, uid: 999999, gid: 999999}}

	w.runCycle(context.Background())
	if len(h.fired) != 1 || h.fired[0]["SERMO_CHANGE"] != "owner" {
		t.Fatalf("owner change not detected: fired=%v", h.fired)
	}
}

func TestFileWatchDeletion(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.txt")
	writeSize(t, f, 1)
	h := &fileWatchHarness{}
	w := h.watcher(f, false, fileCond{onDelete: true})

	w.runCycle(context.Background()) // adopt
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	w.runCycle(context.Background()) // deleted -> fire
	if len(h.fired) != 1 || h.fired[0]["SERMO_CHANGE"] != "deleted" {
		t.Fatalf("deletion not detected: fired=%v", h.fired)
	}
	// The path is dropped, so re-creating it adopts silently (no second fire).
	writeSize(t, f, 1)
	w.runCycle(context.Background())
	if len(h.fired) != 1 {
		t.Fatalf("re-created path should adopt silently, fired=%v", h.fired)
	}
}

func TestFileWatchRecursiveOneEventPerChange(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.txt")
	b := filepath.Join(root, "b.txt")
	writeSize(t, a, 10)
	writeSize(t, b, 10)
	h := &fileWatchHarness{}
	w := h.watcher(root, true, fileCond{sizeChange: true})

	w.runCycle(context.Background()) // adopt root + a + b
	writeSize(t, a, 20)
	writeSize(t, b, 30)
	w.runCycle(context.Background()) // both files changed -> 2 fires

	if len(h.fired) != 2 {
		t.Fatalf("recursive change fired %d times, want 2 (one per changed file)", len(h.fired))
	}
	paths := map[string]bool{}
	for _, env := range h.fired {
		paths[env["SERMO_PATH"]] = true
	}
	if !paths[a] || !paths[b] {
		t.Fatalf("expected a fire per changed file, got paths %v", paths)
	}
}

// TestFileWatchWithRealOSHookRunner exercises the real OSHookRunner (execx-backed)
// inside a fileWatcher, using /bin/true so the hook "succeeds" and emits "hook" event.
func TestFileWatchWithRealOSHookRunner(t *testing.T) {
	f := filepath.Join(t.TempDir(), "size.txt")
	writeSize(t, f, 10)

	var hookEvents []Event
	w := &fileWatcher{
		name:  "fw-real",
		paths: []string{f},
		cond:  fileCond{sizeChange: true},
		hook:  HookSpec{Command: []string{"/bin/true"}, Timeout: time.Second},
		// Use real OSHookRunner (not the test Func) to cover default path + execx.
		runner: OSHookRunner{},
		emit: func(e Event) {
			if e.Kind == eventKindHook || e.Kind == eventKindHookFail {
				hookEvents = append(hookEvents, e)
			}
		},
	}

	// first cycle adopts silently
	w.runCycle(context.Background())
	if len(hookEvents) != 0 {
		t.Fatalf("first cycle should be silent, got events %v", hookEvents)
	}

	// change size -> should fire hook via real runner
	writeSize(t, f, 20)
	w.runCycle(context.Background())

	if len(hookEvents) != 1 || hookEvents[0].Kind != eventKindHook {
		t.Fatalf("expected one successful hook event, got %d: %v", len(hookEvents), hookEvents)
	}
}

// TestFileWatchMissingRootDeletion pins the sensitive "root Lstat fails -> treat
// all baseline entries as gone and fire onDelete if configured" path (used for
// watched config files, data dirs etc that can be removed).
func TestFileWatchMissingRootDeletion(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "watched.txt")
	writeSize(t, f, 42)

	h := &fileWatchHarness{}
	w := h.watcher(f, false, fileCond{onDelete: true, sizeChange: true})

	w.runCycle(context.Background()) // adopt baseline
	if len(h.events) != 0 {
		t.Fatal("adopt must be silent")
	}

	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}

	w.runCycle(context.Background())

	if len(h.events) != 1 || h.events[0].Kind != eventKindHook {
		t.Fatalf("expected one delete hook event, got %d: %v", len(h.events), h.events)
	}
	if msg := h.events[0].Message; msg == "" || !(len(msg) > 0) {
		t.Errorf("delete message = %q", msg)
	}
	// baseline should have been cleaned
	if w.baseline != nil {
		if _, still := w.baseline[f]; still {
			t.Error("baseline entry for deleted root must be removed")
		}
	}
}
