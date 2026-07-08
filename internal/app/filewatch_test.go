package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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
		path:      path,
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
		name: "fw-real",
		path: f,
		cond: fileCond{sizeChange: true},
		hook: HookSpec{Command: []string{"/bin/true"}, Timeout: time.Second},
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
