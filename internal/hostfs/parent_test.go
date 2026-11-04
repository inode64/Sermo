package hostfs

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenParentDirectoryKeepsPinnedDirectory(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenFile(child, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	if err := os.Rename(child, filepath.Join(dir, "renamed")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), child); err != nil {
		t.Fatal(err)
	}
	parent, err := OpenParentDirectory(opened)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	flags, err := unix.FcntlInt(parent.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("parent descriptor is not close-on-exec: %d, %v", flags, err)
	}
	parentMount, parentErr := MountID(parent)
	childMount, childErr := MountID(opened)
	if parentErr != nil || childErr != nil || parentMount == 0 || parentMount != childMount {
		t.Fatalf("pinned mount identities = %d, %d: %v, %v", parentMount, childMount, parentErr, childErr)
	}
	got, err := parent.Stat()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(dir)
	if err != nil || !os.SameFile(got, want) {
		t.Fatalf("parent = %v, %v; want the original parent", got, err)
	}
	if _, err := Open(child + "/.."); err == nil {
		t.Fatal("the path API must still reject traversal")
	}
}

func TestOpenParentDirectoryRejectsInvalidDescriptors(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	if parent, err := OpenParentDirectory(file); err == nil {
		_ = parent.Close()
		t.Fatal("regular file has no directory parent")
	}
	_ = file.Close()
	if _, err := MountID(file); err == nil {
		t.Fatal("closed descriptor must not report a mount")
	}
	if parent, err := OpenParentDirectory(file); err == nil {
		_ = parent.Close()
		t.Fatal("closed descriptor must be rejected")
	}
}
