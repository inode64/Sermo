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

	thirdDone := make(chan error, 1)
	go func() {
		_, err := pool.Acquire(context.Background())
		thirdDone <- err
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
		case err := <-thirdDone:
			t.Fatalf("third acquire finished early: %v", err)
		case <-deadline:
			t.Fatal("timed out waiting for two slot files while third acquire blocks")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	if err := h1.Release(); err != nil {
		t.Fatalf("release h1: %v", err)
	}
	if err := h2.Release(); err != nil {
		t.Fatalf("release h2: %v", err)
	}
	select {
	case err := <-thirdDone:
		if err != nil {
			t.Fatalf("third acquire after release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("third acquire did not complete after releasing slots")
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