package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/locks"
)

func writeWebLockFixture(t *testing.T, dir, fileName string, payload map[string]any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func webLockStartTicks(t *testing.T) uint64 {
	t.Helper()
	ticks, ok := locks.OSProcessProber{}.StartTicks(os.Getpid())
	if !ok {
		t.Skip("cannot read /proc start ticks on this host")
	}
	return ticks
}

func TestWebBackendDetailLocks(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	locksDir := filepath.Join(runtime, "locks")
	expires := time.Now().Add(time.Hour).UTC()

	writeWebLockFixture(t, locksDir, "mysql.backup.lock", map[string]any{
		"service":           "mysql",
		"name":              "backup",
		"reason":            "backup mysql",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": webLockStartTicks(t),
		"expires_at":        expires.Format(time.RFC3339),
	})
	writeWebLockFixture(t, locksDir, "mysql.lock", map[string]any{
		"service":    "mysql",
		"owner_pid":  os.Getpid(),
		"expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	})

	cfg := &config.Config{Global: config.Global{Runtime: runtime}}
	b := &WebBackend{
		order: []string{"mysql"},
		entries: map[string]*webEntry{
			"mysql": {displayName: "mysql"},
		},
		cfg: cfg,
	}

	detail, ok := b.Detail(context.Background(), "mysql")
	if !ok {
		t.Fatal("detail not found")
	}
	if len(detail.Locks) != 2 {
		t.Fatalf("locks = %+v, want 2", detail.Locks)
	}

	byName := map[string]string{}
	for _, lk := range detail.Locks {
		byName[lk.Name] = lk.State
	}
	if byName["backup"] != "active" {
		t.Fatalf("backup lock state = %q, want active", byName["backup"])
	}
	if byName[""] != "expired" {
		t.Fatalf("default lock state = %q, want expired", byName[""])
	}
}

func TestWebBackendDetailLockWarnings(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	locksDir := filepath.Join(runtime, "locks")
	if err := os.MkdirAll(locksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locksDir, "mysql.lock"), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Global: config.Global{Runtime: runtime}}
	b := &WebBackend{
		order:   []string{"mysql"},
		entries: map[string]*webEntry{"mysql": {}},
		cfg:     cfg,
	}

	detail, ok := b.Detail(context.Background(), "mysql")
	if !ok {
		t.Fatal("detail not found")
	}
	if len(detail.LockWarnings) != 1 {
		t.Fatalf("LockWarnings = %+v, want 1 warning", detail.LockWarnings)
	}
}

func TestWebBackendDiagnosticsLockWarnings(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	locksDir := filepath.Join(runtime, "locks")
	if err := os.MkdirAll(locksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locksDir, "redis.lock"), []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := &WebBackend{cfg: &config.Config{Global: config.Global{Runtime: runtime}}}
	findings := b.Diagnostics(context.Background())
	var lockWarn *struct{ Level, Scope, Message string }
	for i := range findings {
		f := findings[i]
		if f.Scope == "locks" && f.Level == "warning" {
			lockWarn = &struct{ Level, Scope, Message string }{f.Level, f.Scope, f.Message}
			break
		}
	}
	if lockWarn == nil {
		t.Fatalf("findings = %+v, want a locks warning", findings)
	}
}

func TestWebBackendViewActiveLocks(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	locksDir := filepath.Join(runtime, "locks")
	expires := time.Now().Add(time.Hour).UTC()

	writeWebLockFixture(t, locksDir, "mysql.backup.lock", map[string]any{
		"service":           "mysql",
		"name":              "backup",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": webLockStartTicks(t),
		"expires_at":        expires.Format(time.RFC3339),
	})
	writeWebLockFixture(t, locksDir, "mysql.lock", map[string]any{
		"service":    "mysql",
		"owner_pid":  os.Getpid(),
		"expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	})

	cfg := &config.Config{Global: config.Global{Runtime: runtime}}
	b := &WebBackend{
		order: []string{"mysql"},
		entries: map[string]*webEntry{
			"mysql": {displayName: "mysql"},
		},
		cfg: cfg,
	}

	svc := b.view(context.Background(), "mysql", b.entries["mysql"])
	if len(svc.ActiveLocks) != 1 || svc.ActiveLocks[0] != "backup" {
		t.Fatalf("ActiveLocks = %+v, want [backup]", svc.ActiveLocks)
	}
}

func TestWebBackendLocksContext(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	locksDir := filepath.Join(runtime, "locks")
	now := time.Now().UTC()

	writeWebLockFixture(t, locksDir, "mysql.backup.lock", map[string]any{
		"service":           "mysql",
		"name":              "backup",
		"reason":            "backup mysql",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": webLockStartTicks(t),
		"created_at":        now.Add(-5 * time.Minute).Format(time.RFC3339),
		"expires_at":        now.Add(time.Hour).Format(time.RFC3339),
	})
	writeWebLockFixture(t, locksDir, "mysql.old.lock", map[string]any{
		"service":           "mysql",
		"name":              "old",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": webLockStartTicks(t),
		"created_at":        now.Add(-2 * time.Hour).Format(time.RFC3339),
		"expires_at":        now.Add(-time.Minute).Format(time.RFC3339),
	})

	b := &WebBackend{
		order:   []string{"mysql"},
		entries: map[string]*webEntry{"mysql": {}},
		cfg:     &config.Config{Global: config.Global{Runtime: runtime}},
	}

	locks := b.Locks(context.Background())
	byName := map[string]struct {
		state       string
		owner       string
		releaseable bool
		blocks      int
	}{}
	for _, lk := range locks {
		byName[lk.Name] = struct {
			state       string
			owner       string
			releaseable bool
			blocks      int
		}{lk.State, lk.OwnerStatus, lk.Releaseable, len(lk.BlockedActions)}
		if lk.Name == "backup" && (lk.TTLRemainingSeconds <= 0 || lk.CreatedAgeSeconds <= 0) {
			t.Fatalf("active lock timing fields missing: %+v", lk)
		}
	}
	if byName["backup"].state != "active" || byName["backup"].owner != "live" || byName["backup"].releaseable || byName["backup"].blocks != 3 {
		t.Fatalf("backup context = %+v", byName["backup"])
	}
	if byName["old"].state != "expired" || !byName["old"].releaseable || byName["old"].blocks != 0 {
		t.Fatalf("old context = %+v", byName["old"])
	}
}

func TestWebBackendReleaseLockOnlyInactive(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	locksDir := filepath.Join(runtime, "locks")
	ticks := webLockStartTicks(t)

	writeWebLockFixture(t, locksDir, "mysql.backup.lock", map[string]any{
		"service":           "mysql",
		"name":              "backup",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": ticks,
		"expires_at":        time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	writeWebLockFixture(t, locksDir, "mysql.old.lock", map[string]any{
		"service":           "mysql",
		"name":              "old",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": ticks,
		"expires_at":        time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	})

	var events []Event
	b := &WebBackend{
		order:   []string{"mysql"},
		entries: map[string]*webEntry{"mysql": {}},
		cfg:     &config.Config{Global: config.Global{Runtime: runtime}},
		emit:    func(e Event) { events = append(events, e) },
	}

	if res := b.ReleaseLock(context.Background(), "mysql", "backup"); res.OK {
		t.Fatalf("active lock release should be blocked: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(locksDir, "mysql.backup.lock")); err != nil {
		t.Fatalf("active lock should remain: %v", err)
	}

	if res := b.ReleaseLock(context.Background(), "mysql", "old"); !res.OK {
		t.Fatalf("expired lock release failed: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(locksDir, "mysql.old.lock")); !os.IsNotExist(err) {
		t.Fatalf("expired lock should be removed: %v", err)
	}
	if len(events) != 2 || events[0].Kind != "suppressed" || events[1].Kind != "action" {
		t.Fatalf("events = %+v", events)
	}
}

func TestWebBackendDetailLocksNone(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{Global: config.Global{Runtime: filepath.Join(root, "run")}}
	b := &WebBackend{
		order:   []string{"web"},
		entries: map[string]*webEntry{"web": {}},
		cfg:     cfg,
	}

	detail, ok := b.Detail(context.Background(), "web")
	if !ok {
		t.Fatal("detail not found")
	}
	if detail.Locks != nil {
		t.Fatalf("locks = %+v, want nil/empty", detail.Locks)
	}
}
