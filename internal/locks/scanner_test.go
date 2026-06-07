package locks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

type fakeProc struct {
	alive map[int]bool
	ticks map[int]uint64
}

func (f fakeProc) Alive(pid int) bool { return f.alive[pid] }

func (f fakeProc) StartTicks(pid int) (uint64, bool) {
	t, ok := f.ticks[pid]
	return t, ok
}

func writeLock(t *testing.T, dir, fileName string, lf lockFile) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(lf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func scannerFor(dir string, proc ProcessProber) Scanner {
	return Scanner{Dir: dir, Proc: proc, Now: func() time.Time { return fixedNow }}
}

func TestScanClassifiesStates(t *testing.T) {
	dir := t.TempDir()
	future := fixedNow.Add(time.Hour)
	past := fixedNow.Add(-time.Hour)

	writeLock(t, dir, "mysql.backup.lock", lockFile{
		Service: "mysql", Name: "backup", Reason: "backup mysql",
		OwnerPID: 100, OwnerStartTicks: 884512, ExpiresAt: future,
	})
	writeLock(t, dir, "mysql.expired.lock", lockFile{
		Service: "mysql", Name: "expired", OwnerPID: 100, OwnerStartTicks: 884512, ExpiresAt: past,
	})
	writeLock(t, dir, "mysql.dead.lock", lockFile{
		Service: "mysql", Name: "dead", OwnerPID: 200, OwnerStartTicks: 884512, ExpiresAt: future,
	})
	writeLock(t, dir, "mysql.reused.lock", lockFile{
		Service: "mysql", Name: "reused", OwnerPID: 100, OwnerStartTicks: 111111, ExpiresAt: future,
	})

	proc := fakeProc{
		alive: map[int]bool{100: true, 200: false},
		ticks: map[int]uint64{100: 884512},
	}

	report, err := scannerFor(dir, proc).Scan("mysql")
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	want := map[string]struct {
		state  State
		reason string
	}{
		"backup":  {StateActive, ""},
		"expired": {StateExpired, "expired"},
		"dead":    {StateStale, "dead owner"},
		"reused":  {StateStale, "pid reuse"},
	}
	if len(report.Locks) != len(want) {
		t.Fatalf("got %d locks, want %d: %+v", len(report.Locks), len(want), report.Locks)
	}
	for _, lock := range report.Locks {
		w, ok := want[lock.Name]
		if !ok {
			t.Errorf("unexpected lock %q", lock.Name)
			continue
		}
		if lock.State != w.state {
			t.Errorf("%s state = %q, want %q", lock.Name, lock.State, w.state)
		}
		if lock.StaleReason != w.reason {
			t.Errorf("%s staleReason = %q, want %q", lock.Name, lock.StaleReason, w.reason)
		}
	}
}

func TestScanMatchesBareAndIgnoresOtherServices(t *testing.T) {
	dir := t.TempDir()
	future := fixedNow.Add(time.Hour)
	writeLock(t, dir, "redis.lock", lockFile{Service: "redis", OwnerPID: 1, ExpiresAt: future})
	writeLock(t, dir, "redis-cache.lock", lockFile{Service: "redis-cache", OwnerPID: 1, ExpiresAt: future})

	proc := fakeProc{alive: map[int]bool{1: true}}
	report, err := scannerFor(dir, proc).Scan("redis")
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(report.Locks) != 1 {
		t.Fatalf("got %d locks, want 1 (must not match redis-cache): %+v", len(report.Locks), report.Locks)
	}
	if report.Locks[0].Name != "" {
		t.Errorf("bare lock Name = %q, want empty", report.Locks[0].Name)
	}
}

func TestScanMissingDirIsEmpty(t *testing.T) {
	report, err := scannerFor(filepath.Join(t.TempDir(), "absent"), fakeProc{}).Scan("mysql")
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(report.Locks) != 0 {
		t.Fatalf("got %d locks, want 0", len(report.Locks))
	}
}

func TestScanDirMalformedFileWarns(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mysql.lock"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	warnings, err := scannerFor(dir, fakeProc{}).ScanDir()
	if err != nil {
		t.Fatalf("ScanDir() error = %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %v", warnings)
	}
}

func TestScanMalformedFileWarns(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mysql.lock"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := scannerFor(dir, fakeProc{}).Scan("mysql")
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(report.Locks) != 0 {
		t.Errorf("malformed file should yield no locks, got %+v", report.Locks)
	}
	if len(report.Warnings) != 1 {
		t.Errorf("expected 1 warning, got %v", report.Warnings)
	}
}

func TestActiveWithoutOwnerPIDAndFutureTTL(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, "mysql.lock", lockFile{Service: "mysql", ExpiresAt: fixedNow.Add(time.Hour)})
	report, err := scannerFor(dir, fakeProc{}).Scan("mysql")
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(report.Locks) != 1 || !report.Locks[0].Active() {
		t.Fatalf("lock with no owner and future TTL should be active: %+v", report.Locks)
	}
}
