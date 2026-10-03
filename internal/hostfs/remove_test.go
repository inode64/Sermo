package hostfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveNoSymlinkAncestors(t *testing.T) {
	for _, recursive := range []bool{false, true} {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := RemoveNoSymlinkAncestors(filepath.Join(link, "child"), recursive); err == nil {
			t.Fatal("followed symlink ancestor")
		}
		if err := RemoveNoSymlinkAncestors(link, recursive); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("removed link target: %v", err)
		}
		if err := RemoveNoSymlinkAncestors(target, recursive); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("target survived: %v", err)
		}
	}
	for _, path := range []string{"/", "relative", "/tmp/../etc"} {
		if err := RemoveNoSymlinkAncestors(path, false); !errors.Is(err, ErrPath) {
			t.Fatalf("unsafe path %q: %v", path, err)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing", "child")
	if err := RemoveNoSymlinkAncestors(missing, true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveNoSymlinkAncestors(missing, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestCleanupParentStaysPinnedAfterReplacement(t *testing.T) {
	dir := t.TempDir()
	parent, other := filepath.Join(dir, "parent"), filepath.Join(dir, "other")
	for _, path := range []string{parent, other} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "file"), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := openDirectoryNoSymlinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	moved := filepath.Join(dir, "moved")
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, parent); err != nil {
		t.Fatal(err)
	}
	if err := root.Remove("file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, "file")); err != nil {
		t.Fatalf("replacement redirected cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moved, "file")); !os.IsNotExist(err) {
		t.Fatalf("pinned target survived: %v", err)
	}
}
