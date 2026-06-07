package checks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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

func TestCountClassifiesEntriesNonRecursive(t *testing.T) {
	root := countTree(t)
	cases := []struct {
		kind string
		want int
	}{
		{countFile, 2},    // a.txt, b.txt (not the symlink, not sub/)
		{countDir, 1},     // sub/
		{countSymlink, 1}, // link
		{countAny, 4},     // a.txt, b.txt, sub/, link
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			n, err := countOf(root, tc.kind, false, "==", 0).tally()
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
	if n, _ := countOf(root, countFile, true, "==", 0).tally(); n != 3 {
		t.Fatalf("recursive file count = %d, want 3", n)
	}
	// Recursive dirs: sub/, sub/nested/ = 2 (root itself never counted).
	if n, _ := countOf(root, countDir, true, "==", 0).tally(); n != 2 {
		t.Fatalf("recursive dir count = %d, want 2", n)
	}
}

func TestCountCheckPredicate(t *testing.T) {
	root := countTree(t)
	// 2 files, threshold "<= 5" holds -> OK.
	if res := countOf(root, countFile, false, "<=", 5).Run(context.Background()); !res.OK {
		t.Fatalf("expected OK for 2 files <= 5, got %q", res.Message)
	}
	// 2 files, threshold ">= 5" does not hold -> not OK.
	if res := countOf(root, countFile, false, ">=", 5).Run(context.Background()); res.OK {
		t.Fatal("expected not OK for 2 files >= 5")
	}
	// Data carries the numeric count under both count and value keys.
	res := countOf(root, countFile, false, ">=", 1).Run(context.Background())
	if res.Data["count"] != 2 || res.Data["value"] != 2 {
		t.Fatalf("data = %+v, want count/value = 2", res.Data)
	}
}

func TestCountCheckMissingPathFails(t *testing.T) {
	res := countOf(filepath.Join(t.TempDir(), "nope"), countAny, false, ">", 0).Run(context.Background())
	if res.OK {
		t.Fatal("count of a missing directory should fail")
	}
}

func TestBuildCountCheck(t *testing.T) {
	root := countTree(t)
	section := map[string]any{
		"files": map[string]any{
			"type":      "count",
			"path":      root,
			"of":        "file",
			"recursive": true,
			"op":        "<=",
			"value":     10,
		},
	}
	built, warns := Build(section, Deps{})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(built) != 1 {
		t.Fatalf("built %d checks, want 1", len(built))
	}
	if !built[0].Check.Run(context.Background()).OK {
		t.Fatal("3 files <= 10 should pass")
	}
}

func TestBuildCountCheckRejectsBadInput(t *testing.T) {
	cases := []map[string]any{
		{"type": "count", "op": ">", "value": 1},                               // no path
		{"type": "count", "path": "/tmp", "op": "><", "value": 1},              // bad op
		{"type": "count", "path": "/tmp", "op": ">", "value": "lots"},          // non-numeric value
		{"type": "count", "path": "/tmp", "of": "pipe", "op": ">", "value": 1}, // bad kind
	}
	for i, entry := range cases {
		_, warns := Build(map[string]any{"c": entry}, Deps{})
		if len(warns) == 0 {
			t.Fatalf("case %d: expected a warning for %v", i, entry)
		}
	}
}
