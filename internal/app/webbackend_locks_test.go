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