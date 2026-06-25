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

// TestSlotPoolDefaultTTL pins ttl<=0 default and sub-second ttl explicit (mutant slotpool .70).
func TestSlotPoolDefaultTTL(t *testing.T) {
	now := time.Unix(50_000, 0)
	proc := fakeProc{alive: map[int]bool{9000: true}, ticks: map[int]uint64{9000: 1}}
	self := func() (int, uint64) { return 9000, 1 }
	t.Run("zero uses default", func(t *testing.T) {
		dir := t.TempDir()
		pool := SlotPool{Dir: dir, Slots: 1, TTL: 0, Proc: proc, Now: func() time.Time { return now }, Self: self, Sleep: time.Sleep}
		h, err := pool.Acquire(context.Background())
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer h.Release()
		got, err := readLockFile(h.path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if want := now.Add(time.Hour); !got.ExpiresAt.Equal(want) {
			t.Fatalf("ExpiresAt = %v want %v", got.ExpiresAt, want)
		}
	})
	t.Run("sub-second ttl stays explicit", func(t *testing.T) {
		dir := t.TempDir()
		pool := SlotPool{Dir: dir, Slots: 1, TTL: time.Nanosecond, Proc: proc, Now: func() time.Time { return now }, Self: self, Sleep: time.Sleep}
		h, err := pool.Acquire(context.Background())
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		defer h.Release()
		got, err := readLockFile(h.path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if want := now.Add(time.Nanosecond); !got.ExpiresAt.Equal(want) {
			t.Fatalf("ExpiresAt = %v want %v", got.ExpiresAt, want)
		}
	})
}

// TestSlotPoolReclaimsExpiredSlot covers the TTL safety net: a slot whose owner
// is still alive but whose ExpiresAt has elapsed must be reclaimable, otherwise
// a wedged owner would hold the slot forever and shrink global concurrency.
func TestSlotPoolReclaimsExpiredSlot(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(10_000, 0)
	proc := fakeProc{
		alive: map[int]bool{4242: true, 7000: true},
		ticks: map[int]uint64{4242: 1, 7000: 9},
	}
	pool := SlotPool{
		Dir:   dir,
		Slots: 1,
		TTL:   time.Hour,
		Proc:  proc,
		Now:   func() time.Time { return now },
		Self:  func() (int, uint64) { return 7000, 9 },
		Sleep: time.Sleep,
	}
	// Existing slot: live owner 4242, but already expired an hour ago.
	writeLock(t, dir, "0.slot", lockFile{
		Service: "slot-0", OwnerPID: 4242, OwnerStartTicks: 1,
		CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	})

	h, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire expired slot: %v", err)
	}
	defer h.Release()

	got, err := readLockFile(h.path)
	if err != nil {
		t.Fatalf("read reclaimed slot: %v", err)
	}
	if got.OwnerPID != 7000 {
		t.Fatalf("slot owner = %d, want 7000 (reclaimed)", got.OwnerPID)
	}
	if got.ExpiresAt.IsZero() {
		t.Fatal("reclaimed slot has no ExpiresAt; TTL safety net not stamped")
	}
	if want := now.Add(time.Hour); !got.ExpiresAt.Equal(want) {
		t.Fatalf("slot ExpiresAt = %v, want %v", got.ExpiresAt, want)
	}
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

func TestNewSlotPoolDefaultsSlots(t *testing.T) {
	// A non-positive slot count defaults to 2; a positive one is preserved.
	if got := NewSlotPool(t.TempDir(), 0).Slots; got != 2 {
		t.Errorf("NewSlotPool(0).Slots = %d, want 2", got)
	}
	if got := NewSlotPool(t.TempDir(), 5).Slots; got != 5 {
		t.Errorf("NewSlotPool(5).Slots = %d, want 5", got)
	}
}
