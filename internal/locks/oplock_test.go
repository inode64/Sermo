package locks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func opLocker(t *testing.T, proc ProcessProber, reclaimed *[]string) OperationLocker {
	t.Helper()
	dir := t.TempDir()
	return OperationLocker{
		Dir:  dir,
		Proc: proc,
		Now:  func() time.Time { return fixedNow },
		Self: func() (int, uint64) { return 5000, 7777 },
		OnReclaim: func(_, reason string) {
			if reclaimed != nil {
				*reclaimed = append(*reclaimed, reason)
			}
		},
	}
}

func TestAcquireOnEmptyDir(t *testing.T) {
	l := opLocker(t, fakeProc{}, nil)
	handle, err := l.Acquire("mysql", time.Hour)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if handle == nil {
		t.Fatal("Acquire() handle = nil")
	}
	lf, err := readLockFile(handle.path)
	if err != nil {
		t.Fatalf("readLockFile: %v", err)
	}
	if lf.OwnerPID != 5000 || lf.OwnerStartTicks != 7777 {
		t.Errorf("owner = %d/%d, want 5000/7777", lf.OwnerPID, lf.OwnerStartTicks)
	}
	if !lf.ExpiresAt.Equal(fixedNow.Add(time.Hour)) {
		t.Errorf("expires_at = %v, want now+1h", lf.ExpiresAt)
	}
}

func TestOperationAcquireRejectsPathLikeService(t *testing.T) {
	root := t.TempDir()
	l := NewOperationLocker(filepath.Join(root, "ops"))

	_, err := l.Acquire("../escape", time.Hour)
	if err == nil || !strings.Contains(err.Error(), "simple name") {
		t.Fatalf("Acquire() error = %v, want simple-name validation error", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "escape.lock")); !os.IsNotExist(statErr) {
		t.Fatalf("path-like service must not create escaped lock file: %v", statErr)
	}
}

func TestAcquireBlockedWhenActive(t *testing.T) {
	reclaimed := []string{}
	l := opLocker(t, fakeProc{alive: map[int]bool{100: true}, ticks: map[int]uint64{100: 884512}}, &reclaimed)
	writeLock(t, l.Dir, "mysql.lock", lockFile{
		Service: "mysql", OwnerPID: 100, OwnerStartTicks: 884512, ExpiresAt: fixedNow.Add(time.Hour),
	})

	_, err := l.Acquire("mysql", time.Hour)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("Acquire() error = %v, want *HeldError", err)
	}
	if held.Lock.OwnerPID != 100 {
		t.Errorf("held lock owner = %d, want 100", held.Lock.OwnerPID)
	}
	if len(reclaimed) != 0 {
		t.Errorf("must not reclaim an active lock, reclaimed = %v", reclaimed)
	}
}

func TestAcquireReclaimsStale(t *testing.T) {
	cases := []struct {
		name    string
		lf      lockFile
		proc    fakeProc
		wantTag string
	}{
		{
			name:    "expired",
			lf:      lockFile{Service: "mysql", OwnerPID: 100, OwnerStartTicks: 884512, ExpiresAt: fixedNow.Add(-time.Hour)},
			proc:    fakeProc{alive: map[int]bool{100: true}, ticks: map[int]uint64{100: 884512}},
			wantTag: "expired",
		},
		{
			name:    "dead owner",
			lf:      lockFile{Service: "mysql", OwnerPID: 200, OwnerStartTicks: 884512, ExpiresAt: fixedNow.Add(time.Hour)},
			proc:    fakeProc{alive: map[int]bool{200: false}},
			wantTag: "dead owner",
		},
		{
			name:    "pid reuse",
			lf:      lockFile{Service: "mysql", OwnerPID: 100, OwnerStartTicks: 111111, ExpiresAt: fixedNow.Add(time.Hour)},
			proc:    fakeProc{alive: map[int]bool{100: true}, ticks: map[int]uint64{100: 884512}},
			wantTag: "pid reuse",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reclaimed := []string{}
			l := opLocker(t, tc.proc, &reclaimed)
			writeLock(t, l.Dir, "mysql.lock", tc.lf)

			handle, err := l.Acquire("mysql", time.Hour)
			if err != nil {
				t.Fatalf("Acquire() error = %v, want reclaim+success", err)
			}
			if len(reclaimed) != 1 || reclaimed[0] != tc.wantTag {
				t.Fatalf("reclaim reasons = %v, want [%s]", reclaimed, tc.wantTag)
			}
			lf, err := readLockFile(handle.path)
			if err != nil {
				t.Fatalf("readLockFile: %v", err)
			}
			if lf.OwnerPID != 5000 {
				t.Errorf("after reclaim owner = %d, want 5000 (us)", lf.OwnerPID)
			}
		})
	}
}

func TestReleaseRemovesOwnLock(t *testing.T) {
	l := opLocker(t, fakeProc{}, nil)
	handle, err := l.Acquire("mysql", time.Hour)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if err := handle.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := os.Stat(handle.path); !os.IsNotExist(err) {
		t.Fatalf("lock file still present after Release: %v", err)
	}
	// Release is idempotent.
	if err := handle.Release(); err != nil {
		t.Fatalf("second Release() error = %v", err)
	}
}

func TestReleaseLeavesForeignLock(t *testing.T) {
	l := opLocker(t, fakeProc{}, nil)
	handle, err := l.Acquire("mysql", time.Hour)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	// Simulate another process reclaiming and taking the lock.
	writeLock(t, l.Dir, "mysql.lock", lockFile{Service: "mysql", OwnerPID: 6000, OwnerStartTicks: 1})

	if err := handle.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	lf, err := readLockFile(handle.path)
	if err != nil {
		t.Fatalf("foreign lock should remain: %v", err)
	}
	if lf.OwnerPID != 6000 {
		t.Errorf("foreign owner = %d, want 6000 (untouched)", lf.OwnerPID)
	}
}

// TestAcquireRealSelfBlocksSecond exercises the real atomic create and the real
// /proc prober: holding a live lock blocks a second acquisition.
func TestAcquireRealSelfBlocksSecond(t *testing.T) {
	l := NewOperationLocker(t.TempDir())

	handle, err := l.Acquire("mysql", time.Hour)
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	defer handle.Release()

	_, err = l.Acquire("mysql", time.Hour)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("second Acquire() error = %v, want *HeldError", err)
	}

	// After releasing, a fresh acquisition succeeds.
	if err := handle.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	again, err := l.Acquire("mysql", time.Hour)
	if err != nil {
		t.Fatalf("re-Acquire() after release error = %v", err)
	}
	_ = again.Release()
}

// flipProc reports a PID as dead on the first probe and alive thereafter,
// simulating an owner that becomes active between the stale check and the unlink.
type flipProc struct {
	pid   int
	calls *int
}

func (f flipProc) Alive(pid int) bool {
	if pid != f.pid {
		return false
	}
	*f.calls++
	return *f.calls > 1
}

func (flipProc) StartTicks(int) (uint64, bool) { return 0, false }

func TestAcquireReclaimRaceAbortsAsHeld(t *testing.T) {
	calls := 0
	reclaimed := []string{}
	l := opLocker(t, flipProc{pid: 200, calls: &calls}, &reclaimed)
	writeLock(t, l.Dir, "mysql.lock", lockFile{
		Service: "mysql", OwnerPID: 200, OwnerStartTicks: 884512, ExpiresAt: fixedNow.Add(time.Hour),
	})

	_, err := l.Acquire("mysql", time.Hour)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("Acquire() error = %v, want *HeldError (race lost)", err)
	}
	if len(reclaimed) != 0 {
		t.Errorf("must not report reclaim when the race was lost, got %v", reclaimed)
	}
}
