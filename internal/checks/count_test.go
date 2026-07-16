package checks

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// countTree builds a temp dir:
//
//	root/
//	  a.txt, b.txt          (2 files)
//	  sub/                  (1 dir)
//	    c.txt               (1 file, only reachable recursively)
//	    nested/             (1 dir, recursive)
//	  link -> a.txt         (1 symlink)
func countTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "sub", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "c.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	return root
}

func countOf(root, kind string, recursive bool, op string, value float64) countCheck {
	return countCheck{
		base:      base{name: "c"},
		path:      root,
		kind:      kind,
		recursive: recursive,
		op:        op,
		value:     value,
	}
}

func addCountFiles(t *testing.T, root, prefix string, n int) {
	t.Helper()
	for i := range n {
		path := filepath.Join(root, prefix+"-"+strconv.Itoa(i))
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func removeCountFiles(t *testing.T, root, prefix string, n int) {
	t.Helper()
	for i := range n {
		if err := os.Remove(filepath.Join(root, prefix+"-"+strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
}

func deltaCountOf(root string, now *time.Time, op string, value float64, window time.Duration) countCheck {
	return countCheck{
		base:       base{name: "c"},
		path:       root,
		kind:       CountKindFile,
		deltaOp:    op,
		deltaValue: value,
		window:     window,
		clock:      func() time.Time { return *now },
		state:      &countState{},
	}
}

func TestCountClassifiesEntriesNonRecursive(t *testing.T) {
	root := countTree(t)
	cases := []struct {
		kind string
		want int
	}{
		{CountKindFile, 2},    // a.txt, b.txt (not the symlink, not sub/)
		{CountKindDir, 1},     // sub/
		{CountKindSymlink, 1}, // link
		{CountKindAny, 4},     // a.txt, b.txt, sub/, link
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			n, err := countOf(root, tc.kind, false, "==", 0).tally(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if n != tc.want {
				t.Fatalf("%s count = %d, want %d", tc.kind, n, tc.want)
			}
		})
	}
}

func TestCountRecursiveDescendsSubtree(t *testing.T) {
	root := countTree(t)
	// Recursive files: a.txt, b.txt, sub/c.txt = 3.
	if n, _ := countOf(root, CountKindFile, true, "==", 0).tally(context.Background()); n != 3 {
		t.Fatalf("recursive file count = %d, want 3", n)
	}
	// Recursive dirs: sub/, sub/nested/ = 2 (root itself never counted).
	if n, _ := countOf(root, CountKindDir, true, "==", 0).tally(context.Background()); n != 2 {
		t.Fatalf("recursive dir count = %d, want 2", n)
	}
}

// writeSizedTree writes each file (path relative to root) with the given byte
// size, creating parent directories.
func writeSizedTree(t *testing.T, root string, files map[string]int) {
	t.Helper()
	for path, size := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCountRecursiveSkipsHiddenEntriesByDefault(t *testing.T) {
	root := t.TempDir()
	// Content size is irrelevant to counting — only visibility matters.
	writeSizedTree(t, root, map[string]int{"visible.txt": 1, ".hidden.txt": 1, ".cache/nested.txt": 1})

	c := countOf(root, CountKindFile, true, "==", 0)
	if n, err := c.tally(context.Background()); err != nil || n != 1 {
		t.Fatalf("default recursive count = %d, %v; want 1, nil", n, err)
	}
	c.includeHidden = true
	if n, err := c.tally(context.Background()); err != nil || n != 3 {
		t.Fatalf("include_hidden recursive count = %d, %v; want 3, nil", n, err)
	}
}

func TestCountDeltaWithinWindowAlertsOnGrowth(t *testing.T) {
	root := t.TempDir()
	addCountFiles(t, root, "base", 1)
	now := time.Unix(0, 0)
	c := deltaCountOf(root, &now, ">", 2, 2*time.Minute)

	if res := c.Run(context.Background()); res.OK {
		t.Fatalf("first cycle must only baseline: %s", res.Message)
	}

	now = now.Add(time.Minute)
	addCountFiles(t, root, "batch-a", 2)
	if res := c.Run(context.Background()); res.OK {
		t.Fatalf("growth equal to the > threshold must not alert: %s", res.Message)
	}

	now = now.Add(time.Minute)
	addCountFiles(t, root, "batch-b", 1)
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("growth over the threshold should alert: %s", res.Message)
	}
	if res.Data[DataKeyCount] != 4 ||
		res.Data[DataKeyBaselineCount] != 1 ||
		res.Data[DataKeyGrowthCount] != 3 {
		t.Fatalf("data = %+v, want count=4 baseline_count=1 growth_count=3", res.Data)
	}
	if res.Data[DataKeyWindow] != (2 * time.Minute).String() {
		t.Fatalf("window data = %v, want 2m0s", res.Data[DataKeyWindow])
	}
}

func TestCountDeltaWindowPrunesOldGrowth(t *testing.T) {
	root := t.TempDir()
	addCountFiles(t, root, "base", 1)
	now := time.Unix(0, 0)
	c := deltaCountOf(root, &now, ">", 2, 2*time.Minute)

	_ = c.Run(context.Background())
	now = now.Add(3 * time.Minute)
	addCountFiles(t, root, "old", 3)
	res := c.Run(context.Background())
	if res.OK {
		t.Fatalf("growth before the window must be pruned: %s", res.Message)
	}
	if res.Data[DataKeyBaselineCount] != 4 || res.Data[DataKeyGrowthCount] != 0 {
		t.Fatalf("data = %+v, want baseline_count=4 growth_count=0", res.Data)
	}
}

func TestCountDeltaDecreaseDoesNotAlert(t *testing.T) {
	root := t.TempDir()
	addCountFiles(t, root, "base", 4)
	now := time.Unix(0, 0)
	c := deltaCountOf(root, &now, "<", 10, 2*time.Minute)

	_ = c.Run(context.Background())
	now = now.Add(time.Minute)
	removeCountFiles(t, root, "base", 3)
	if res := c.Run(context.Background()); res.OK {
		t.Fatalf("shrinking count must not alert even if the comparison would match: %s", res.Message)
	}
}

func TestCountCheckHonorsCanceledContext(t *testing.T) {
	root := countTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := countOf(root, CountKindAny, true, ">", 0).Run(ctx)
	if res.OK {
		t.Fatal("canceled count check should fail")
	}
	if !strings.Contains(res.Message, "cancelled") {
		t.Fatalf("message = %q, want cancelled", res.Message)
	}
}

func TestCountCheckPredicate(t *testing.T) {
	root := countTree(t)
	// 2 files, threshold "<= 5" holds -> OK.
	if res := countOf(root, CountKindFile, false, "<=", 5).Run(context.Background()); !res.OK {
		t.Fatalf("expected OK for 2 files <= 5, got %q", res.Message)
	}
	// 2 files, threshold ">= 5" does not hold -> not OK. The "want" value is
	// rendered with FormatFloat -1 precision, so an integral threshold has no
	// trailing ".0" (e.g. "5", not "5.0").
	if res := countOf(root, CountKindFile, false, ">=", 5).Run(context.Background()); res.OK {
		t.Fatal("expected not OK for 2 files >= 5")
	} else if want := "2 file entries in"; !strings.Contains(res.Message, want) || !strings.Contains(res.Message, "(want >= 5)") {
		t.Fatalf("message = %q, want %q and (want >= 5)", res.Message, want)
	}
	// Data carries the numeric count under both count and value keys.
	res := countOf(root, CountKindFile, false, ">=", 1).Run(context.Background())
	if res.Data["count"] != 2 || res.Data["value"] != 2 {
		t.Fatalf("data = %+v, want count/value = 2", res.Data)
	}
}

func TestCountCheckMissingPathFails(t *testing.T) {
	res := countOf(filepath.Join(t.TempDir(), "nope"), CountKindAny, false, ">", 0).Run(context.Background())
	if res.OK {
		t.Fatal("count of a missing directory should fail")
	}
}

func TestBuildCountCheck(t *testing.T) {
	root := countTree(t)
	section := map[string]any{
		"files": map[string]any{
			"type":           "count",
			"path":           root,
			"of":             "file",
			"recursive":      true,
			"include_hidden": true,
			"op":             "<=",
			"value":          10,
		},
	}
	built, warns := Build(section, Deps{})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 {
		t.Fatalf("built %d checks, want 1", len(built))
	}
	count, ok := built[0].Check.(countCheck)
	if !ok || !count.includeHidden {
		t.Fatalf("built = %T %+v, want include_hidden", built[0].Check, built[0].Check)
	}
	if !built[0].Check.Run(context.Background()).OK {
		t.Fatal("3 files <= 10 should pass")
	}
}

func TestBuildCountDeltaCheck(t *testing.T) {
	root := countTree(t)
	section := map[string]any{
		"growth": map[string]any{
			"type":   "count",
			"path":   root,
			"of":     "file",
			"delta":  map[string]any{"op": ">", "value": 2},
			"within": "2m",
		},
	}
	built, warns := Build(section, Deps{})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 {
		t.Fatalf("built %d checks, want 1", len(built))
	}
	c, ok := built[0].Check.(countCheck)
	if !ok {
		t.Fatalf("built check = %T, want countCheck", built[0].Check)
	}
	if c.deltaOp != ">" || c.deltaValue != 2 || c.window != 2*time.Minute || c.state == nil {
		t.Fatalf("built delta count = %+v, want op > value 2 window 2m state", c)
	}
	if res := c.Run(context.Background()); res.OK {
		t.Fatalf("first delta run must baseline only: %s", res.Message)
	}
}

func TestBuildCountCheckRejectsBadInput(t *testing.T) {
	cases := []map[string]any{
		{"type": "count", "op": ">", "value": 1},                               // no path
		{"type": "count", "path": "/tmp", "op": "><", "value": 1},              // bad op
		{"type": "count", "path": "/tmp", "op": ">", "value": "lots"},          // non-numeric value
		{"type": "count", "path": "/tmp", "of": "pipe", "op": ">", "value": 1}, // bad kind
		{
			"type": "count", "path": "/tmp",
			"delta": map[string]any{"op": ">", "value": 1},
		}, // no within
		{
			"type": "count", "path": "/tmp",
			"delta": map[string]any{"op": ">", "value": 1}, "within": "nope",
		}, // bad within
		{
			"type": "count", "path": "/tmp",
			"delta": map[string]any{"op": "><", "value": 1}, "within": "2m",
		}, // bad delta op
		{
			"type": "count", "path": "/tmp",
			"delta": map[string]any{"op": ">", "value": "lots"}, "within": "2m",
		}, // bad delta value
		{
			"type": "count", "path": "/tmp",
			"count": map[string]any{"op": ">", "value": 1},
			"delta": map[string]any{"op": ">", "value": 1}, "within": "2m",
		}, // mixed modes
		{
			"type": "count", "path": "/tmp", "op": ">", "value": 1,
			"delta": map[string]any{"op": ">", "value": 1}, "within": "2m",
		}, // mixed modes
		{
			"type": "count", "path": "/tmp", "op": ">", "value": 1, "within": "2m",
		}, // window without delta
	}
	for i, entry := range cases {
		_, warns := Build(map[string]any{"c": entry}, Deps{})
		if len(warns) == 0 {
			t.Fatalf("case %d: expected a warning for %v", i, entry)
		}
	}
}
