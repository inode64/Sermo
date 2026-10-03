package locks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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

func TestAcquireDerivesExpiryFromCreation(t *testing.T) {
	dir := t.TempDir()
	created := fixedNow.Add(time.Minute)
	l := OperationLocker{
		Dir:  dir,
		Proc: fakeProc{},
		Now:  func() time.Time { return created },
		Self: func() (int, uint64) { return 5000, 7777 },
	}
	h, err := l.Acquire("mysql", time.Hour)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	lf, err := readLockFile(h.path)
	if err != nil {
		t.Fatalf("readLockFile: %v", err)
	}
	if !lf.ExpiresAt.Equal(lf.CreatedAt.Add(time.Hour)) {
		t.Fatalf("expires_at = %v, want created_at + 1h (%v)", lf.ExpiresAt, lf.CreatedAt.Add(time.Hour))
	}
}

func TestOperationAcquireRejectsPathLikeService(t *testing.T) {
	root := t.TempDir()
	l := NewOperationLocker(RuntimeOpsDir(root))

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
	held, ok := errors.AsType[*HeldError](err)
	if !ok {
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
			wantTag: staleReasonExpired,
		},
		{
			name:    "dead owner",
			lf:      lockFile{Service: "mysql", OwnerPID: 200, OwnerStartTicks: 884512, ExpiresAt: fixedNow.Add(time.Hour)},
			proc:    fakeProc{alive: map[int]bool{200: false}},
			wantTag: staleReasonDeadOwner,
		},
		{
			name:    "pid reuse",
			lf:      lockFile{Service: "mysql", OwnerPID: 100, OwnerStartTicks: 111111, ExpiresAt: fixedNow.Add(time.Hour)},
			proc:    fakeProc{alive: map[int]bool{100: true}, ticks: map[int]uint64{100: 884512}},
			wantTag: staleReasonPIDReuse,
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

// holdDirExclusion takes the same directory flock another release or reclaim
// holds, through its own open file description as a concurrent goroutine or
// process would.
func holdDirExclusion(t *testing.T, dir string) func() {
	t.Helper()
	d, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(d.Fd()), unix.LOCK_EX); err != nil {
		_ = d.Close()
		t.Fatal(err)
	}
	return func() {
		_ = unix.Flock(int(d.Fd()), unix.LOCK_UN)
		_ = d.Close()
	}
}

// A release that meets a momentary exclusion held by another service's release
// must not strand this service's lock until its TTL.
func TestReleaseWaitsOutBriefDirectoryContention(t *testing.T) {
	l := opLocker(t, fakeProc{}, nil)
	handle, err := l.Acquire("mysql", time.Hour)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	unlock := holdDirExclusion(t, l.Dir)
	timer := time.AfterFunc(20*time.Millisecond, unlock)
	defer timer.Stop()

	if err := handle.Release(); err != nil {
		t.Fatalf("Release() under brief contention error = %v", err)
	}
	if _, err := os.Stat(handle.path); !os.IsNotExist(err) {
		t.Fatalf("lock file still present after Release: %v", err)
	}
}

// Persistent contention stays bounded and never removes without exclusion.
func TestReleaseFailsBoundedUnderPersistentContention(t *testing.T) {
	l := opLocker(t, fakeProc{}, nil)
	handle, err := l.Acquire("mysql", time.Hour)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	unlock := holdDirExclusion(t, l.Dir)
	defer unlock()

	start := time.Now()
	if err := handle.Release(); err == nil {
		t.Fatal("Release() without directory exclusion succeeded")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Release() waited %s, want a bounded retry", elapsed)
	}
	if _, err := os.Stat(handle.path); err != nil {
		t.Fatalf("lock must remain without exclusion: %v", err)
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
	defer func() { _ = handle.Release() }()

	_, err = l.Acquire("mysql", time.Hour)
	if _, ok := errors.AsType[*HeldError](err); !ok {
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
	if _, ok := errors.AsType[*HeldError](err); !ok {
		t.Fatalf("Acquire() error = %v, want *HeldError (race lost)", err)
	}
	if len(reclaimed) != 0 {
		t.Errorf("must not report reclaim when the race was lost, got %v", reclaimed)
	}
}

func TestExclusivePublicationNeverExposesPartialPayload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mysql.lock")
	payload := lockFile{Service: "mysql", Reason: strings.Repeat("x", 1<<20), ExpiresAt: fixedNow.Add(time.Hour)}
	done := make(chan error, 1)
	go func() { done <- writeLockFileExclusive(path, payload) }()
	deadline := time.After(time.Second)
	for {
		lf, err := readLockFile(path)
		if err == nil {
			if lf.Reason != payload.Reason || !lf.ExpiresAt.Equal(payload.ExpiresAt) {
				t.Fatal("published an incomplete lock")
			}
			break
		}
		if !isMissingLock(err) {
			t.Fatalf("reader saw partial publication: %v", err)
		}
		select {
		case <-deadline:
			t.Fatal("publication timed out")
		default:
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := writeLockFileExclusive(path, lockFile{Service: "other"}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second publication = %v, want exists", err)
	}
	if lf, err := readLockFile(path); err != nil || lf.Service != payload.Service {
		t.Fatalf("overwrote original: %+v %v", lf, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("staging files leaked: %v %v", entries, err)
	}
}

func TestFailedAndAbandonedLockStagingDoesNotHoldService(t *testing.T) {
	l := opLocker(t, fakeProc{}, nil)
	path := filepath.Join(l.Dir, "mysql.lock")
	invalid := lockFile{CreatedAt: time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)}
	if err := writeLockFileExclusive(path, invalid); err == nil {
		t.Fatal("invalid timestamp was published")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed publication left a lock: %v", err)
	}
	if err := os.WriteFile(path+".abandoned.tmp", []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	scanner := Scanner{Dir: l.Dir, Proc: fakeProc{}, Now: l.Now}
	if report, err := scanner.Scan("mysql"); err != nil || len(report.Locks)+len(report.Warnings) != 0 {
		t.Fatalf("abandoned staging blocked scanner: %+v %v", report, err)
	}
	h, err := l.Acquire("mysql", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptPublishedLockReportsParseError(t *testing.T) {
	l := opLocker(t, fakeProc{}, nil)
	path := filepath.Join(l.Dir, "mysql.lock")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire("mysql", time.Hour); err == nil || isHeld(err) || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("corrupt lock = %v, want parse diagnostic", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "{" {
		t.Fatalf("corrupt lock was removed: %q %v", data, err)
	}
}

func TestOwnedLocksRejectUnknownStartTime(t *testing.T) {
	for _, pid := range []int{0, 5000} {
		l := opLocker(t, fakeProc{}, nil)
		l.Self = func() (int, uint64) { return pid, 0 }
		if _, err := l.Acquire("mysql", time.Hour); err == nil {
			t.Fatal("operation lock accepted unknown owner identity")
		}
		named := NamedLocker{Dir: l.Dir, Self: l.Self, Proc: fakeProc{}, Now: l.Now}
		if _, err := named.Hold("mysql", "backup", "", time.Hour); err == nil {
			t.Fatal("named lock accepted unknown owner identity")
		}
		if entries, err := os.ReadDir(l.Dir); err != nil || len(entries) != 0 {
			t.Fatalf("failed acquisition created files: %v %v", entries, err)
		}
		if _, err := named.Pin("mysql", "backup", "", time.Hour); err != nil {
			t.Fatalf("ownerless persistent lock rejected: %v", err)
		}
	}
}

func TestLegacyUnknownOwnerStartTimeIsNotPIDReuse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		alive   bool
		ticks   uint64
		expired bool
		want    State
	}{
		{"read recovers", true, 123, false, StateActive},
		{"read unavailable", true, 0, false, StateActive},
		{"dead owner", false, 0, false, StateStale},
		{"expired", true, 123, true, StateExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lf := lockFile{OwnerPID: 100, ExpiresAt: fixedNow.Add(time.Hour)}
			if tc.expired {
				lf.ExpiresAt = fixedNow
			}
			proc := fakeProc{alive: map[int]bool{100: tc.alive}, ticks: map[int]uint64{100: tc.ticks}}
			if got, reason := classify(lf, fixedNow, proc); got != tc.want {
				t.Fatalf("state=%s reason=%s, want %s", got, reason, tc.want)
			}
			l := opLocker(t, proc, nil)
			writeLock(t, l.Dir, "mysql.lock", lf)
			_, err := l.Acquire("mysql", time.Hour)
			if (tc.want == StateActive) != isHeld(err) {
				t.Fatalf("Acquire=%v, expected state=%s", err, tc.want)
			}
		})
	}
}
