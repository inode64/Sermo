package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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

// fakeOwnerTicks is a stand-in start-ticks value for lock fixtures. With
// fakeAliveProber installed the value is never compared (StartTicks reports
// ok=false), so any constant keeps the fixtures realistic without /proc.
const fakeOwnerTicks = uint64(12345)

// fakeAliveProber reports every PID as alive and declines to read start ticks,
// so classify() treats non-expired locks as active without touching /proc. This
// keeps the lock-view tests deterministic on hosts without /proc.
type fakeAliveProber struct{}

func (fakeAliveProber) Alive(int) bool                { return true }
func (fakeAliveProber) StartTicks(int) (uint64, bool) { return 0, false }

// useFakeLockProber installs fakeAliveProber for the duration of the test so the
// web backend's lock scans no longer depend on the host's /proc.
func useFakeLockProber(t *testing.T) {
	t.Helper()
	old := lockProcProber
	lockProcProber = fakeAliveProber{}
	t.Cleanup(func() { lockProcProber = old })
}

func TestWebBackendDetailLocks(t *testing.T) {
	useFakeLockProber(t)
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	locksDir := locks.RuntimeLocksDir(runtime)
	expires := time.Now().Add(time.Hour).UTC()

	writeWebLockFixture(t, locksDir, "mysql\\backup.lock", map[string]any{
		"service":           "mysql",
		"name":              "backup",
		"reason":            "backup mysql",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": fakeOwnerTicks,
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
	locksDir := locks.RuntimeLocksDir(runtime)
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

func TestWebBackendViewActiveLocks(t *testing.T) {
	useFakeLockProber(t)
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	locksDir := locks.RuntimeLocksDir(runtime)
	expires := time.Now().Add(time.Hour).UTC()

	writeWebLockFixture(t, locksDir, "mysql\\backup.lock", map[string]any{
		"service":           "mysql",
		"name":              "backup",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": fakeOwnerTicks,
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

func TestWebBackendServicesActiveLocks(t *testing.T) {
	useFakeLockProber(t)
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	locksDir := locks.RuntimeLocksDir(runtime)
	expires := time.Now().Add(time.Hour).UTC()
	ticks := fakeOwnerTicks

	writeWebLockFixture(t, locksDir, "mysql\\backup.lock", map[string]any{
		"service":           "mysql",
		"name":              "backup",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": ticks,
		"expires_at":        expires.Format(time.RFC3339),
	})
	writeWebLockFixture(t, locksDir, "redis.lock", map[string]any{
		"service":           "redis",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": ticks,
		"expires_at":        expires.Format(time.RFC3339),
	})
	writeWebLockFixture(t, locksDir, "redis\\old.lock", map[string]any{
		"service":    "redis",
		"name":       "old",
		"owner_pid":  os.Getpid(),
		"expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	})

	b := &WebBackend{
		order: []string{"mysql", "redis"},
		entries: map[string]*webEntry{
			"mysql": {},
			"redis": {},
		},
		cfg: &config.Config{Global: config.Global{Runtime: runtime}},
	}

	services := b.Services(context.Background())
	byName := map[string][]string{}
	for _, svc := range services {
		byName[svc.Name] = svc.ActiveLocks
	}
	if len(byName["mysql"]) != 1 || byName["mysql"][0] != "backup" {
		t.Fatalf("mysql ActiveLocks = %+v, want [backup]", byName["mysql"])
	}
	if len(byName["redis"]) != 1 || byName["redis"][0] != "(default)" {
		t.Fatalf("redis ActiveLocks = %+v, want [(default)]", byName["redis"])
	}
}

func TestWebBackendLocksContext(t *testing.T) {
	useFakeLockProber(t)
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	locksDir := locks.RuntimeLocksDir(runtime)
	now := time.Now().UTC()

	writeWebLockFixture(t, locksDir, "mysql\\backup.lock", map[string]any{
		"service":           "mysql",
		"name":              "backup",
		"reason":            "backup mysql",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": fakeOwnerTicks,
		"created_at":        now.Add(-5 * time.Minute).Format(time.RFC3339),
		"expires_at":        now.Add(time.Hour).Format(time.RFC3339),
	})
	writeWebLockFixture(t, locksDir, "mysql\\old.lock", map[string]any{
		"service":           "mysql",
		"name":              "old",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": fakeOwnerTicks,
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
		blocks      []string
	}{}
	for _, lk := range locks {
		byName[lk.Name] = struct {
			state       string
			owner       string
			releaseable bool
			blocks      []string
		}{lk.State, lk.OwnerStatus, lk.Releaseable, lk.BlockedActions}
		if lk.Name == "backup" && (lk.TTLRemainingSeconds <= 0 || lk.CreatedAgeSeconds <= 0) {
			t.Fatalf("active lock timing fields missing: %+v", lk)
		}
	}
	if byName["backup"].state != "active" || byName["backup"].owner != "live" || byName["backup"].releaseable || !slices.Equal(byName["backup"].blocks, serviceOperationActionList()) {
		t.Fatalf("backup context = %+v", byName["backup"])
	}
	if byName["old"].state != "expired" || !byName["old"].releaseable || len(byName["old"].blocks) != 0 {
		t.Fatalf("old context = %+v", byName["old"])
	}
}

func TestWebBackendLocksSeveralServices(t *testing.T) {
	useFakeLockProber(t)
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	locksDir := locks.RuntimeLocksDir(runtime)
	expires := time.Now().Add(time.Hour).UTC()
	ticks := fakeOwnerTicks

	writeWebLockFixture(t, locksDir, "mysql.lock", map[string]any{
		"service":           "mysql",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": ticks,
		"expires_at":        expires.Format(time.RFC3339),
	})
	writeWebLockFixture(t, locksDir, "redis\\cache.lock", map[string]any{
		"service":           "redis",
		"name":              "cache",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": ticks,
		"expires_at":        expires.Format(time.RFC3339),
	})
	writeWebLockFixture(t, locksDir, "disabled.lock", map[string]any{
		"service":           "disabled",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": ticks,
		"expires_at":        expires.Format(time.RFC3339),
	})

	b := &WebBackend{
		order: []string{"mysql", "redis", "disabled"},
		entries: map[string]*webEntry{
			"mysql":    {},
			"redis":    {},
			"disabled": {disabled: true},
		},
		cfg: &config.Config{Global: config.Global{Runtime: runtime}},
	}

	locks := b.Locks(context.Background())
	got := map[string]string{}
	for _, lk := range locks {
		got[lk.Service] = lk.Name
	}
	if got["mysql"] != "" || got["redis"] != "cache" {
		t.Fatalf("locks = %+v, want mysql default and redis cache", locks)
	}
	if _, ok := got["disabled"]; ok {
		t.Fatalf("disabled service lock should not be listed: %+v", locks)
	}
}

func TestWebBackendReleaseLockOnlyInactive(t *testing.T) {
	useFakeLockProber(t)
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	locksDir := locks.RuntimeLocksDir(runtime)
	ticks := fakeOwnerTicks

	writeWebLockFixture(t, locksDir, "mysql\\backup.lock", map[string]any{
		"service":           "mysql",
		"name":              "backup",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": ticks,
		"expires_at":        time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})
	writeWebLockFixture(t, locksDir, "mysql\\old.lock", map[string]any{
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
	if _, err := os.Stat(filepath.Join(locksDir, "mysql\\backup.lock")); err != nil {
		t.Fatalf("active lock should remain: %v", err)
	}

	if res := b.ReleaseLock(context.Background(), "mysql", "old"); !res.OK {
		t.Fatalf("expired lock release failed: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(locksDir, "mysql\\old.lock")); !os.IsNotExist(err) {
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
