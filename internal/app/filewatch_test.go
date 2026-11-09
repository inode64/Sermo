package app

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx/execxtest"
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

func scannedFiles(w *fileWatcher) int {
	current, _ := w.scan(time.Now(), func() {})
	return fileWatchNumberFiles(current)
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
	w := h.watcher(f, false, fileCond{sizeChange: true, permChange: true, ownerChange: true})
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
	// Every file hook carries the watch's grade, per-file older_than included.
	if h.fired[0][sermoEnvSeverity] != "error" {
		t.Fatalf("stale path hook severity = %q, want error", h.fired[0][sermoEnvSeverity])
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

func TestFileWatchRecursiveOlderThanDatesRegularFilesOnly(t *testing.T) {
	// A GeoIP directory kept a 2019 compatibility link (GeoIPCity.dat ->
	// GeoLiteCity.dat) next to databases refreshed weekly: the link's own mtime
	// failed the freshness watch forever. Links and directories carry no data
	// age; only regular files are judged, and the reading is the oldest file.
	dir := t.TempDir()
	now := time.Now().Add(3 * time.Hour) // the link, the subdirectory and dir are 3h old
	db := filepath.Join(dir, "GeoLite2-City.mmdb")
	writeSize(t, db, 1)
	if err := os.Chtimes(db, now.Add(-10*time.Minute), now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("GeoLiteCity.dat", filepath.Join(dir, "GeoIPCity.dat")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := &fileWatchHarness{}
	w := h.watcher(dir, true, fileCond{olderThan: time.Hour})
	w.summary = "GeoIP ${value} old in ${number_files} files"
	w.check = map[string]any{checks.CheckKeyOlderThan: "1h"}
	w.now = func() time.Time { return now }
	var latest checks.Result
	w.publish = func(_, _ string, res checks.Result) { latest = res }

	w.runCycle(t.Context())

	if len(h.fired) != 0 || !latest.OK {
		t.Fatalf("only a fresh database is present: fired %v, result %+v", h.fired, latest)
	}
	if latest.Message != "GeoIP 10m old in 1 files" || latest.Data[checks.DataKeyAge] != "10m" {
		t.Fatalf("the reading must be the oldest file's age, got %q / %v", latest.Message, latest.Data[checks.DataKeyAge])
	}

	if err := os.Chtimes(db, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	w.runCycle(t.Context())
	if len(h.fired) != 1 || h.fired[0][sermoEnvPath] != db || latest.OK {
		t.Fatalf("a stale regular file must still fire: fired %v, result %+v", h.fired, latest)
	}
}

func TestFileWatchNonRecursiveOlderThanKeepsTheNamedPath(t *testing.T) {
	// A path named on its own keeps its mtime contract whatever its kind: an
	// untouched spool directory is the condition such a watch reports.
	dir := t.TempDir()
	now := time.Now().Add(3 * time.Hour)
	h := &fileWatchHarness{}
	w := h.watcher(dir, false, fileCond{olderThan: time.Hour})
	w.now = func() time.Time { return now }

	w.runCycle(t.Context())

	if len(h.fired) != 1 || h.fired[0][sermoEnvPath] != dir {
		t.Fatalf("a stale named directory must fire, fired %v", h.fired)
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

// With several roots the reading describes the root it measured, and the entry
// count excludes every present root, not just one.
func TestFileWatchSnapshotNamesTheMeasuredRoot(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "missing")
	first, second := filepath.Join(base, "first"), filepath.Join(base, "second")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSize(t, filepath.Join(first, "a.txt"), 10)
	writeSize(t, filepath.Join(second, "b.txt"), 10)
	h := &fileWatchHarness{}
	w := h.watcher(missing, true, fileCond{sizeChange: true})
	w.paths = []string{missing, first, second}
	w.absentOK = true
	var got checks.Result
	w.publish = func(_, _ string, res checks.Result) { got = res }

	w.runCycle(context.Background())

	if !strings.HasPrefix(got.Message, first+" size ") {
		t.Fatalf("message = %q, want the size of %s, the root it measured", got.Message, first)
	}
	if got.Data[watchReadingFieldEntries] != 2 {
		t.Fatalf("entries = %v, want 2 (the two files, without either root)", got.Data[watchReadingFieldEntries])
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
	if got := scannedFiles(w); got != 1 {
		t.Fatalf("default recursive files = %d, want 1", got)
	}
	w.includeHidden = true
	if got := scannedFiles(w); got != 3 {
		t.Fatalf("include_hidden recursive files = %d, want 3", got)
	}

	hiddenRoot := filepath.Join(root, ".explicit")
	if err := os.Mkdir(hiddenRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSize(t, filepath.Join(hiddenRoot, "visible.txt"), 1)
	w.paths = []string{hiddenRoot}
	w.includeHidden = false
	if got := scannedFiles(w); got != 1 {
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

	if age := got.Data[checks.DataKeyAge]; age != "25h" {
		t.Fatalf("snapshot age = %v, want 25h", age)
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

// ENOTDIR is the kernel saying the path cannot exist: a parent directory that
// became a file took the watched path away as surely as removing it.
func TestFileWatchParentReplacedByAFileIsADeletion(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "current")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(parent, "config.yml")
	writeSize(t, f, 1)
	h := &fileWatchHarness{}
	w := h.watcher(f, false, fileCond{onDelete: true})
	var latest checks.Result
	w.publish = func(_, _ string, res checks.Result) { latest = res }

	w.runCycle(context.Background())
	if err := os.RemoveAll(parent); err != nil {
		t.Fatal(err)
	}
	writeSize(t, parent, 1)

	w.runCycle(context.Background())
	if len(h.fired) != 1 || h.fired[0]["SERMO_CHANGE"] != "deleted" {
		t.Fatalf("fired = %v, want the deletion", h.fired)
	}
	if latest.Observation() == checks.ObservationUnavailable || !strings.Contains(latest.Message, "not found") {
		t.Fatalf("snapshot = %+v, want the path reported absent", latest)
	}
}

// Only a path that is gone is a deletion. Any other read failure says nothing
// about the file, so it must not run the delete hook, and the unchanged file is
// not news once it can be read again. A symlink loop in the parent directory
// fails the read with ELOOP whichever user runs the test.
func TestFileWatchUnreadablePathIsNotADeletion(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "export")
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(parent, "data")
	writeSize(t, f, 1)
	h := &fileWatchHarness{}
	w := h.watcher(f, false, fileCond{onDelete: true, sizeChange: true})
	var latest checks.Result
	w.publish = func(_, _ string, res checks.Result) { latest = res }

	w.runCycle(context.Background())
	aside := parent + ".aside"
	if err := os.Rename(parent, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(parent, parent); err != nil {
		t.Fatal(err)
	}

	w.runCycle(context.Background())
	if len(h.fired) != 0 {
		t.Fatalf("an unreadable path fired %v", h.fired)
	}
	if latest.Observation() != checks.ObservationUnavailable || strings.Contains(latest.Message, "not found") {
		t.Fatalf("snapshot = %+v, want the read failure reported as unavailable", latest)
	}

	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(aside, parent); err != nil {
		t.Fatal(err)
	}
	w.runCycle(context.Background())
	if len(h.fired) != 0 || latest.Observation() != checks.ObservationHealthy {
		t.Fatalf("readable again: fired %v, snapshot %+v", h.fired, latest)
	}

	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	w.runCycle(context.Background())
	if len(h.fired) != 1 || h.fired[0]["SERMO_CHANGE"] != fileChangeDeleted {
		t.Fatalf("a real deletion fired %v", h.fired)
	}
}

// An unreadable directory hides its entries; it does not delete them. A subtree
// that was never readable tracks nothing, so it stays the best-effort skip it
// always was and does not turn the watch unavailable.
func TestFileWatchUnreadableSubtreeKeepsItsEntries(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory whatever its mode")
	}
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	inner := filepath.Join(sub, "inner")
	writeSize(t, inner, 1)
	gone := filepath.Join(root, "gone")
	writeSize(t, gone, 1)
	h := &fileWatchHarness{}
	w := h.watcher(root, true, fileCond{onDelete: true})
	var latest checks.Result
	w.publish = func(_, _ string, res checks.Result) { latest = res }

	w.runCycle(context.Background())
	if err := os.Chmod(sub, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	w.runCycle(context.Background())
	if len(h.fired) != 1 || h.fired[0]["SERMO_PATH"] != gone {
		t.Fatalf("fired %v, want only the entry really deleted", h.fired)
	}
	if latest.Observation() != checks.ObservationUnavailable {
		t.Fatalf("snapshot = %+v, want unavailable while a tracked entry is out of sight", latest)
	}

	blind := h.watcher(root, true, fileCond{onDelete: true})
	var fresh checks.Result
	blind.publish = func(_, _ string, res checks.Result) { fresh = res }
	blind.runCycle(context.Background())
	if fresh.Observation() != checks.ObservationHealthy {
		t.Fatalf("snapshot of a subtree never read = %+v, want the skip it always was", fresh)
	}
}

// hungScanWatcher is a file watch whose scan blocks like a hard mount gone dead.
func hungScanWatcher(h *fileWatchHarness, hung *hungRead, path string, baseline ...string) *fileWatcher {
	w := h.watcher(path, false, fileCond{onDelete: true, sizeChange: true})
	w.scanTimeout = 20 * time.Millisecond
	w.lstat = func(string) (fs.FileInfo, error) {
		hung.block()
		return nil, fs.ErrNotExist
	}
	w.baseline = map[string]fileState{}
	for _, path := range baseline {
		w.baseline[path] = fileState{size: 1}
	}
	return w
}

// A scan blocked in the kernel takes no context, so the cycle has to stop waiting
// for it: a stuck cycle held every reload and shutdown behind the generation's join.
func TestFileWatchHungScanLeavesTheMonitorRunning(t *testing.T) {
	hung := newHungRead(t)
	w := hungScanWatcher(&fileWatchHarness{}, hung, filepath.Join(t.TempDir(), "report"))
	assertMonitorSurvives(t, hung, &Watch{Name: w.name, Cycle: w.runCycle})
}

// A scan that gave no answer reported nothing about the paths: it is neither a
// deletion nor a recovery, and it must read as unavailable until one comes.
func TestFileWatchHungScanReadsAsUnavailable(t *testing.T) {
	hung := newHungRead(t)
	h := &fileWatchHarness{}
	path := filepath.Join(t.TempDir(), "report")
	w := hungScanWatcher(h, hung, path, path)
	var latest checks.Result
	w.publish = func(_, _ string, res checks.Result) { latest = res }

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runCycle(context.Background())
	}()
	awaitWithin(t, done, "a cycle over a hung scan")

	if len(h.fired) != 0 {
		t.Fatalf("a hung scan fired %v", h.fired)
	}
	if latest.Observation() != checks.ObservationUnavailable || !strings.Contains(latest.Message, path) {
		t.Fatalf("snapshot = %+v, want the hung path reported unavailable", latest)
	}
	if _, kept := w.baseline[path]; !kept {
		t.Fatal("a hung scan dropped the path's baseline")
	}
}

// A tree too large to read within the timeout still answers entry by entry. Each
// attempt at it would time out the same way, so reading it as hung would leave a
// healthy watch unavailable for good.
func TestFileWatchSlowScanIsNotHung(t *testing.T) {
	const (
		entries = 6
		timeout = 150 * time.Millisecond
	)
	dir := t.TempDir()
	paths := make([]string, 0, entries)
	for i := range entries {
		path := filepath.Join(dir, strconv.Itoa(i))
		writeSize(t, path, 1)
		paths = append(paths, path)
	}
	h := &fileWatchHarness{}
	w := h.watcher(paths[0], false, fileCond{onDelete: true})
	w.paths, w.scanTimeout = paths, timeout
	w.lstat = func(path string) (fs.FileInfo, error) {
		time.Sleep(timeout / 4)
		return os.Lstat(path)
	}
	var latest checks.Result
	w.publish = func(_, _ string, res checks.Result) { latest = res }

	w.runCycle(context.Background())

	if latest.Observation() != checks.ObservationHealthy || len(w.baseline) != entries {
		t.Fatalf("snapshot = %+v baseline = %d, want every entry read", latest, len(w.baseline))
	}
}

// The limit reads these steps, so a recursive walk has to report each entry.
func TestFileWatchScanStepsThroughATree(t *testing.T) {
	root := t.TempDir()
	const entries = 3
	for i := range entries {
		writeSize(t, filepath.Join(root, strconv.Itoa(i)), 1)
	}
	w := (&fileWatchHarness{}).watcher(root, true, fileCond{onDelete: true})
	steps := 0
	w.scan(time.Now(), func() { steps++ })
	if steps <= entries {
		t.Fatalf("steps = %d, want one for the root and each of %d entries", steps, entries)
	}
}

// A scan that keeps going is no reason to hold a reload: the generation's join
// waits on this cycle, whatever the scan's own limit.
func TestFileWatchCancelledCycleStopsWaitingForItsScan(t *testing.T) {
	hung := newHungRead(t)
	h := &fileWatchHarness{}
	path := filepath.Join(t.TempDir(), "report")
	w := hungScanWatcher(h, hung, path, path)
	w.scanTimeout = time.Hour
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runCycle(ctx)
	}()
	cancel()
	awaitWithin(t, done, "a cancelled cycle over a running scan")
	if len(h.fired) != 0 {
		t.Fatalf("a cancelled cycle fired %v", h.fired)
	}
	if _, kept := w.baseline[path]; !kept {
		t.Fatal("a cancelled cycle dropped the path's baseline")
	}
}

// A reload rebuilds the watch while the old scan is still blocked. The new
// watcher must wait for that one, not start a second on the same dead mount.
func TestFileWatchRebuiltDuringAHungScanDoesNotStackAnother(t *testing.T) {
	hung := newHungRead(t)
	h := &fileWatchHarness{}
	path := filepath.Join(t.TempDir(), "report")
	first := hungScanWatcher(h, hung, path, path)
	second := hungScanWatcher(h, hung, path, path)
	for _, w := range []*fileWatcher{first, second} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			w.runCycle(context.Background())
		}()
		awaitWithin(t, done, "a cycle over a hung scan")
	}
	if got := hung.calls.Load(); got != 1 {
		t.Fatalf("blocked reads = %d, want the first one only", got)
	}
	if len(h.fired) != 0 {
		t.Fatalf("a hung scan fired %v", h.fired)
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
// inside a fileWatcher over a scripted runner, so the hook "succeeds" and emits a
// "hook" event carrying the watched path.
func TestFileWatchWithRealOSHookRunner(t *testing.T) {
	fake := &execxtest.Runner{}
	f := filepath.Join(t.TempDir(), "size.txt")
	writeSize(t, f, 10)

	var hookEvents []Event
	w := &fileWatcher{
		name:  "fw-real",
		paths: []string{f},
		cond:  fileCond{sizeChange: true},
		hook:  HookSpec{Command: []string{"/usr/local/bin/on-change"}, Timeout: time.Second},
		// Use the real OSHookRunner (not the test Func) to cover the execx path.
		runner: OSHookRunner{Runner: fake},
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
	if calls := fake.Calls(); len(calls) != 1 || calls[0].Line() != "/usr/local/bin/on-change" {
		t.Fatalf("hook calls = %+v", calls)
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
	if msg := h.events[0].Message; msg == "" {
		t.Errorf("delete message = %q", msg)
	}
	// baseline should have been cleaned
	if w.baseline != nil {
		if _, still := w.baseline[f]; still {
			t.Error("baseline entry for deleted root must be removed")
		}
	}
}

func TestFileWatchSizeThresholdFiresForAPathThatArrivesBreaching(t *testing.T) {
	// A file whose very appearance is the event — /root/dead.letter is the
	// canonical case — was adopted silently and never fired, because the
	// threshold only ran for paths already in the baseline. An absolute
	// condition can be judged from a single observation, which is the rule
	// older_than already follows for a path that is stale when first seen.
	f := filepath.Join(t.TempDir(), "dead.letter")
	h := &fileWatchHarness{}
	w := h.watcher(f, false, fileCond{sizeOp: ">", sizeValue: 0})

	w.runCycle(context.Background()) // absent
	if len(h.fired) != 0 {
		t.Fatalf("an absent path must not fire, got %v", h.fired)
	}

	writeSize(t, f, 500)
	w.runCycle(context.Background())
	if len(h.fired) != 1 {
		t.Fatalf("a path that arrives over the threshold must fire once, got %d", len(h.fired))
	}
	if h.fired[0]["SERMO_CHANGE"] != "size_threshold" || h.fired[0]["SERMO_SIZE"] != "500" {
		t.Fatalf("unexpected threshold env: %v", h.fired[0])
	}

	// Still breaching: edge-triggered, so it must not repeat.
	w.runCycle(context.Background())
	if len(h.fired) != 1 {
		t.Fatalf("threshold fired %d times while still breaching, want 1", len(h.fired))
	}
}

func TestFileWatchSizeThresholdFiresAfterObserveOnlyCycle(t *testing.T) {
	// A file already over the threshold when the daemon starts. The settling
	// cycle must observe without reporting, and must not record the breach as
	// the prior state — doing so made the first real cycle see no edge and stay
	// silent forever, which is the whole dead.letter-survives-a-restart case.
	// This mirrors TestFileWatchOlderThanFiresAfterObserveOnlyCycle.
	f := filepath.Join(t.TempDir(), "dead.letter")
	writeSize(t, f, 500)
	h := &fileWatchHarness{}
	w := h.watcher(f, false, fileCond{sizeOp: ">", sizeValue: 0})

	w.runCycle(withObserveOnly(context.Background(), true))
	if len(h.fired) != 0 {
		t.Fatalf("the settling cycle must observe silently, got %v", h.fired)
	}
	w.runCycle(context.Background())
	if len(h.fired) != 1 {
		t.Fatalf("a path already breaching at startup must fire once it settles, got %d", len(h.fired))
	}
	w.runCycle(context.Background())
	if len(h.fired) != 1 {
		t.Fatalf("threshold fired %d times while still breaching, want 1", len(h.fired))
	}
}

func TestFileWatchAbsentPathIsFailureByDefault(t *testing.T) {
	// The default contract: the watch asserts its paths exist, which is what a
	// config file or a database directory needs.
	var got checks.Result
	w := &fileWatcher{
		name:    "fw",
		paths:   []string{filepath.Join(t.TempDir(), "nope")},
		cond:    fileCond{sizeOp: ">", sizeValue: 0},
		publish: func(_, _ string, r checks.Result) { got = r },
	}
	w.runCycle(context.Background())
	if got.OK {
		t.Fatalf("a missing watched path must fail by default, got %+v", got)
	}
}

func TestFileWatchAbsentOKReportsHealthyUntilTheFileAppears(t *testing.T) {
	// /root/dead.letter is the canonical case: the file not existing is exactly
	// what healthy looks like, so the watch must not sit red on every host that
	// has nothing wrong with it — while still firing once the file shows up.
	dir := t.TempDir()
	f := filepath.Join(dir, "dead.letter")
	var got checks.Result
	h := &fileWatchHarness{}
	w := h.watcher(f, false, fileCond{sizeOp: ">", sizeValue: 0})
	w.absentOK = true
	w.publish = func(_, _ string, r checks.Result) { got = r }

	w.runCycle(context.Background())
	if !got.OK {
		t.Fatalf("an absent path must be healthy with absent_ok, got %+v", got)
	}
	if len(h.fired) != 0 {
		t.Fatalf("an absent path must not fire, got %v", h.fired)
	}

	writeSize(t, f, 500)
	w.runCycle(context.Background())
	if len(h.fired) != 1 {
		t.Fatalf("the file appearing over the threshold must still fire, got %d", len(h.fired))
	}
	if got.OK {
		t.Fatalf("a present file over its size threshold must remain unhealthy, got %+v", got)
	}
}
