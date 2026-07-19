package operation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A recursive clean must refuse to delete through a symlinked ancestor so a
// planted parent symlink cannot redirect the delete to another tree.
func TestCleanStopPathRefusesSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	// link -> real; deleting link/data would traverse the symlink.
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(link, "data")

	warns := cleanStopPath(CleanPath{Path: target, Recursive: true})
	if len(warns) != 1 || !strings.Contains(warns[0], "symlink") {
		t.Fatalf("warns = %v, want a refusal naming the symlink", warns)
	}
	// The real tree must be untouched.
	if _, err := os.Stat(filepath.Join(real, "data")); err != nil {
		t.Fatalf("real data was deleted through the symlink: %v", err)
	}
}

// Without a symlinked ancestor the recursive clean proceeds.
func TestCleanStopPathDeletesRealTree(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "svc", "cache")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if warns := cleanStopPath(CleanPath{Path: target, Recursive: true}); len(warns) != 0 {
		t.Fatalf("warns = %v, want none", warns)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target should be deleted, stat err = %v", err)
	}
}
