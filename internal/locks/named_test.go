package locks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func namedLocker(dir string, proc ProcessProber) NamedLocker {
	return NamedLocker{
		Dir:  dir,
		Proc: proc,
		Now:  func() time.Time { return fixedNow },
		Self: func() (int, uint64) { return 5000, 7777 },
	}
}

func TestNamedPinIsActiveWithoutOwner(t *testing.T) {
	l := namedLocker(t.TempDir(), fakeProc{})
	path, err := l.Pin("mysql", "backup", "nightly backup", time.Hour)
	if err != nil {
		t.Fatalf("Pin() error = %v", err)
	}

	lf, err := readLockFile(path)
	if err != nil {
		t.Fatalf("readLockFile: %v", err)
	}
	if lf.OwnerPID != 0 {
		t.Errorf("persistent lock owner = %d, want 0 (no live owner)", lf.OwnerPID)
	}

	// The scanner classifies it active (future TTL, no owner) — the engine blocks.
	report, _ := Scanner{Dir: l.Dir, Proc: fakeProc{}, Now: func() time.Time { return fixedNow }}.Scan("mysql")
	if len(report.Locks) != 1 || !report.Locks[0].Active() {
		t.Fatalf("pinned lock should be active: %+v", report.Locks)
	}
	if report.Locks[0].Name != "backup" || report.Locks[0].Reason != "nightly backup" {
		t.Errorf("lock metadata = %+v", report.Locks[0])
	}

	if err := l.Release("mysql", "backup"); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lock should be removed after Release: %v", err)
	}
	// Release of a missing lock is not an error.
	if err := l.Release("mysql", "backup"); err != nil {
		t.Fatalf("second Release() error = %v", err)
	}
}

func TestNamedLockerRejectsPathLikeIDs(t *testing.T) {
	root := t.TempDir()
	l := namedLocker(RuntimeLocksDir(root), fakeProc{})

	tests := []struct {
		name    string
		service string
		lock    string
	}{
		{name: "service traversal", service: "../escape", lock: ""},
		{name: "service separator", service: "mysql/main", lock: ""},
		{name: "lock traversal", service: "mysql", lock: "../backup"},
		{name: "lock separator", service: "mysql", lock: "backup/nightly"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := l.Pin(tc.service, tc.lock, "x", time.Hour); err == nil || !strings.Contains(err.Error(), "simple name") {
				t.Fatalf("Pin() error = %v, want simple-name validation error", err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, "escape.lock")); !os.IsNotExist(err) {
		t.Fatalf("path-like service must not create escaped lock file: %v", err)
	}
	if err := l.Release("mysql", "../backup"); err == nil || !strings.Contains(err.Error(), "simple name") {
		t.Fatalf("Release() error = %v, want simple-name validation error", err)
	}
}

func TestNamedHoldBlocksSecond(t *testing.T) {
	l := namedLocker(t.TempDir(), fakeProc{alive: map[int]bool{5000: true}, ticks: map[int]uint64{5000: 7777}})
	h, err := l.Hold("mysql", "", "work", time.Hour)
	if err != nil {
		t.Fatalf("Hold() error = %v", err)
	}
	defer func() { _ = h.Release() }()

	_, err = l.Hold("mysql", "", "work", time.Hour)
	if _, ok := errors.AsType[*HeldError](err); !ok {
		t.Fatalf("second Hold() error = %v, want *HeldError", err)
	}
}

func TestNamedReclaimsExpired(t *testing.T) {
	l := namedLocker(t.TempDir(), fakeProc{})
	writeLock(t, l.Dir, "mysql\\backup.lock", lockFile{
		Service: "mysql", Name: "backup", ExpiresAt: fixedNow.Add(-time.Hour), // expired
	})
	path, err := l.Pin("mysql", "backup", "again", time.Hour)
	if err != nil {
		t.Fatalf("Pin() over expired lock error = %v", err)
	}
	if lf, _ := readLockFile(path); lf.Reason != "again" {
		t.Fatalf("expired lock should have been reclaimed and rewritten, got %+v", lf)
	}
}

func TestNamedReleaseInactiveRemovesExpiredAndStale(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		payload  lockFile
		proc     fakeProc
		wantName string
	}{
		{
			name: "expired",
			file: "mysql\\backup.lock",
			payload: lockFile{
				Service: "mysql", Name: "backup", OwnerPID: 100, OwnerStartTicks: 1,
				ExpiresAt: fixedNow.Add(-time.Minute),
			},
			proc:     fakeProc{alive: map[int]bool{100: true}, ticks: map[int]uint64{100: 1}},
			wantName: "backup",
		},
		{
			name: "stale",
			file: "mysql.lock",
			payload: lockFile{
				Service: "mysql", OwnerPID: 200, OwnerStartTicks: 1,
				ExpiresAt: fixedNow.Add(time.Hour),
			},
			proc:     fakeProc{alive: map[int]bool{200: false}},
			wantName: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := namedLocker(t.TempDir(), tt.proc)
			writeLock(t, l.Dir, tt.file, tt.payload)

			lk, err := l.ReleaseInactive("mysql", tt.wantName)
			if err != nil {
				t.Fatalf("ReleaseInactive() error = %v", err)
			}
			if lk.State == StateActive {
				t.Fatalf("released lock state = %q, want inactive", lk.State)
			}
			if _, err := os.Stat(filepath.Join(l.Dir, tt.file)); !os.IsNotExist(err) {
				t.Fatalf("lock file should be removed: %v", err)
			}
		})
	}
}

func TestNamedReleaseInactiveRefusesActive(t *testing.T) {
	l := namedLocker(t.TempDir(), fakeProc{alive: map[int]bool{100: true}, ticks: map[int]uint64{100: 1}})
	writeLock(t, l.Dir, "mysql\\backup.lock", lockFile{
		Service: "mysql", Name: "backup", OwnerPID: 100, OwnerStartTicks: 1,
		ExpiresAt: fixedNow.Add(time.Hour),
	})

	lk, err := l.ReleaseInactive("mysql", "backup")
	if err == nil {
		t.Fatal("ReleaseInactive() should refuse an active lock")
	}
	if lk.State != StateActive {
		t.Fatalf("lock state = %q, want active", lk.State)
	}
	if _, statErr := os.Stat(filepath.Join(l.Dir, "mysql\\backup.lock")); statErr != nil {
		t.Fatalf("active lock should remain: %v", statErr)
	}
}

func TestNamedReleaseMissingIsNotError(t *testing.T) {
	// Releasing a lock that was never acquired is a no-op, not an error
	// (os.Remove's not-exist error is swallowed).
	l := namedLocker(t.TempDir(), fakeProc{})
	if err := l.Release("mysql", "deploy"); err != nil {
		t.Fatalf("Release of a missing lock must not error, got %v", err)
	}
}
