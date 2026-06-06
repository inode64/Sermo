package locks

import (
	"errors"
	"os"
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

func TestNamedHoldBlocksSecond(t *testing.T) {
	l := namedLocker(t.TempDir(), fakeProc{alive: map[int]bool{5000: true}, ticks: map[int]uint64{5000: 7777}})
	h, err := l.Hold("mysql", "", "work", time.Hour)
	if err != nil {
		t.Fatalf("Hold() error = %v", err)
	}
	defer h.Release()

	_, err = l.Hold("mysql", "", "work", time.Hour)
	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("second Hold() error = %v, want *HeldError", err)
	}
}

func TestNamedReclaimsExpired(t *testing.T) {
	l := namedLocker(t.TempDir(), fakeProc{})
	writeLock(t, l.Dir, "mysql.backup.lock", lockFile{
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
