package locks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSlotPoolAcquireAndRelease(t *testing.T) {
	dir := t.TempDir()
	pool := NewSlotPool(dir, 2)

	h1, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	h2, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}

	thirdDone := make(chan *SlotHandle, 1)
	go func() {
		h, err := pool.Acquire(context.Background())
		if err != nil {
			thirdDone <- nil
			return
		}
		thirdDone <- h
	}()

	deadline := time.After(100 * time.Millisecond)
	for {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir: %v", err)
		}
		if len(entries) == 2 {
			break
		}
		select {
		case h := <-thirdDone:
			if h != nil {
				t.Fatalf("third acquire finished early")
			}
		case <-deadline:
			t.Fatal("timed out waiting for two slot files while third acquire blocks")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	inUse, err := pool.InUse()
	if err != nil {
		t.Fatalf("InUse while held: %v", err)
	}
	if inUse != 2 {
		t.Fatalf("InUse = %d, want 2", inUse)
	}

	if err := h1.Release(); err != nil {
		t.Fatalf("release h1: %v", err)
	}
	if err := h2.Release(); err != nil {
		t.Fatalf("release h2: %v", err)
	}
	var h3 *SlotHandle
	select {
	case h3 = <-thirdDone:
		if h3 == nil {
			t.Fatal("third acquire after release failed")
		}
	case <-time.After(time.Second):
		t.Fatal("third acquire did not complete after releasing slots")
	}
	if err := h3.Release(); err != nil {
		t.Fatalf("release h3: %v", err)
	}

	inUse, err = pool.InUse()
	if err != nil {
		t.Fatalf("InUse after release: %v", err)
	}
	if inUse != 0 {
		t.Fatalf("InUse after release = %d, want 0", inUse)
	}
}

func TestSlotPoolReclaimsDeadOwner(t *testing.T) {
	dir := t.TempDir()
	proc := fakeProc{alive: map[int]bool{4242: false}}
	pool := SlotPool{
		Dir:   dir,
		Slots: 1,
		Proc:  proc,
		Now:   func() time.Time { return time.Unix(0, 0) },
		Self:  func() (int, uint64) { return 4242, 1 },
		Sleep: time.Sleep,
	}
	path := filepath.Join(dir, "0.slot")
	writeLock(t, dir, "0.slot", lockFile{
		Service: "slot-0", OwnerPID: 4242, OwnerStartTicks: 1,
		CreatedAt: time.Unix(0, 0),
	})

	h, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire after reclaim: %v", err)
	}
	if h.path != path {
		t.Fatalf("handle path = %q, want %q", h.path, path)
	}
	_ = h.Release()
}

// TestSlotHandleReleaseLeavesForeignSlot locks the owner-checked release on
// the slot side: a slot file rewritten by another owner (reclaim) must be
// left untouched, and a nil handle must release as a no-op.
func TestSlotHandleReleaseLeavesForeignSlot(t *testing.T) {
	dir := t.TempDir()
	pool := NewSlotPool(dir, 1)
	h, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// Simulate a reclaim: another owner now holds the same slot file.
	foreign := lockFile{Service: "slot-0", OwnerPID: h.ownerPID + 1, OwnerStartTicks: h.ownerStartTicks + 1}
	if err := os.Remove(h.path); err != nil {
		t.Fatal(err)
	}
	if err := writeLockFileExclusive(h.path, foreign); err != nil {
		t.Fatalf("rewrite as foreign owner: %v", err)
	}

	if err := h.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	current, err := readLockFile(h.path)
	if err != nil {
		t.Fatalf("the foreign slot must survive this owner's release: %v", err)
	}
	if current.OwnerPID != foreign.OwnerPID {
		t.Fatalf("slot owner = %d, want the foreign owner %d", current.OwnerPID, foreign.OwnerPID)
	}

	var nilHandle *SlotHandle
	if err := nilHandle.Release(); err != nil {
		t.Fatalf("nil handle release must be a no-op, got %v", err)
	}
}
