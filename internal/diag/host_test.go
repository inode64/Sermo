package diag

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOSHostPathExists(t *testing.T) {
	h := OSHost{}
	present := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !h.PathExists(present) || h.PathExists(present+".missing") {
		t.Fatal("PathExists misreports")
	}
}

func TestOSHostInterfaceExists(t *testing.T) {
	h := OSHost{}
	// The loopback interface always exists on Linux.
	if !h.InterfaceExists("lo") {
		t.Skip("no lo interface on this host")
	}
	if h.InterfaceExists("sermo-definitely-missing0") {
		t.Fatal("nonexistent interface reported present")
	}
}

func TestOSHostIsMountPoint(t *testing.T) {
	h := OSHost{}
	if !h.IsMountPoint("/") {
		t.Fatal("/ must be a mount point")
	}
	if h.IsMountPoint(t.TempDir()) {
		t.Fatal("a fresh temp dir must not be a mount point")
	}
}
