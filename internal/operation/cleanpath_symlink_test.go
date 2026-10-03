package operation

import (
	"os"
	"path/filepath"
	"sermo/internal/config"
	"strings"
	"testing"
)

// A recursive clean must refuse to delete through a symlinked ancestor so a
// planted parent symlink cannot redirect the delete to another tree.
func TestCleanStopPathRefusesSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(realDir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	// link -> real; deleting link/data would traverse the symlink.
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(link, "data")

	warns := cleanStopPath(config.CleanPath{Path: target, Recursive: true})
	if len(warns) != 1 || !strings.Contains(warns[0], "symlink") {
		t.Fatalf("warns = %v, want a refusal naming the symlink", warns)
	}
	// The real tree must be untouched.
	if _, err := os.Stat(filepath.Join(realDir, "data")); err != nil {
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
	if warns := cleanStopPath(config.CleanPath{Path: target, Recursive: true}); len(warns) != 0 {
		t.Fatalf("warns = %v, want none", warns)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target should be deleted, stat err = %v", err)
	}
}

func TestAllStopArtifactCleanupRejectsSymlinkAncestors(t *testing.T) {
	for _, kind := range []string{"file", "glob", "pidfile", "files_absent"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			realDir := filepath.Join(root, "real")
			if err := os.Mkdir(realDir, 0o700); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(realDir, "demo.pid")
			if err := os.WriteFile(victim, []byte("retained"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "link")
			if err := os.Symlink(realDir, link); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(link, "demo.pid")
			var warnings []string
			switch kind {
			case "file":
				warnings = cleanStopPath(config.CleanPath{Path: path})
			case "glob":
				warnings = cleanStopPath(config.CleanPath{Path: filepath.Join(link, "*.pid")})
			case "pidfile", "files_absent":
				e := Engine{StopArtifacts: config.StopArtifacts{CleanEnabled: true}}
				warnings = e.stoppedPathWarnings(path, kind == "files_absent")
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], "symlink") {
				t.Fatalf("warnings=%v", warnings)
			}
			if data, err := os.ReadFile(victim); err != nil || string(data) != "retained" {
				t.Fatalf("victim=%q error=%v", data, err)
			}
		})
	}
}
