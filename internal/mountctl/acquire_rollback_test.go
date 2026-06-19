package mountctl

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestAcquireUnmountsOnStateWriteFailure pins that when Acquire mounts on the
// 0->1 edge but cannot persist the new refcount, it rolls the mount back. Before
// the fix the filesystem stayed mounted while the persisted refcount remained 0,
// so a later Release would unmount it out from under a still-active user.
func TestAcquireUnmountsOnStateWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based state-write failure injection needs non-root")
	}
	mounted := false
	runner := &fakeRunner{mounted: &mounted}
	c := testController(t, &mounted, runner)

	// Make only the mounts/state dir read-only so writeState's WriteFile fails,
	// while the operation lock (mounts/ops) and readState (a missing state file)
	// still work.
	runtime := t.TempDir()
	stateDir := filepath.Join(runtime, "mounts", "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })
	c.Runtime = runtime

	spec := Spec{Name: "backup", Path: "/mnt/backup", Refcount: true}
	if _, err := c.Acquire(context.Background(), spec); err == nil {
		t.Fatal("Acquire should fail when the refcount cannot be persisted")
	}
	if mounted {
		t.Fatal("a mount whose refcount could not be persisted must be rolled back (unmounted)")
	}
	var didMount, didUmount bool
	for _, call := range runner.calls {
		switch call {
		case "mount /mnt/backup":
			didMount = true
		case "umount /mnt/backup":
			didUmount = true
		}
	}
	if !didMount || !didUmount {
		t.Fatalf("calls = %v, want a mount followed by a compensating umount", runner.calls)
	}
}
