package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/locks"
)

// selfStartTicks returns this process's start time so a fixture can name the
// running test binary as a live, non-reused lock owner.
func selfStartTicks(t *testing.T) uint64 {
	t.Helper()
	ticks, ok := locks.OSProcessProber{}.StartTicks(os.Getpid())
	if !ok {
		t.Skip("cannot read /proc start ticks on this host")
	}
	return ticks
}

// writeLocksConfig builds a config whose runtime points at root, and returns the
// global config path plus the locks directory.
func writeLocksConfig(t *testing.T, root string) (string, string) {
	t.Helper()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  includes: [ `+root+`/enabled ]
  runtime: `+root+`/run
defaults:
  policy:
    cooldown: 5m
`)
	return global, filepath.Join(root, "run", "locks")
}

func writeLockFixture(t *testing.T, dir, fileName string, payload map[string]any) {
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

func TestLocksReportsActiveLock(t *testing.T) {
	root := t.TempDir()
	global, locksDir := writeLocksConfig(t, root)
	writeLockFixture(t, locksDir, "mysql\\backup.lock", map[string]any{
		"service":           "mysql",
		"name":              "backup",
		"reason":            "backup mysql",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": selfStartTicks(t),
		"expires_at":        time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})

	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	code := app.Run(context.Background(), []string{"--config", global, "locks", "mysql"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	out := stdout.String()
	if !strings.Contains(out, "mysql.backup active") {
		t.Fatalf("stdout = %q, want active backup lock", out)
	}
	if !strings.Contains(out, `reason="backup mysql"`) {
		t.Fatalf("stdout = %q, want reason", out)
	}
}

func TestLocksReportsExpiredLock(t *testing.T) {
	root := t.TempDir()
	global, locksDir := writeLocksConfig(t, root)
	writeLockFixture(t, locksDir, "mysql.lock", map[string]any{
		"service":    "mysql",
		"owner_pid":  os.Getpid(),
		"expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	})

	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	code := app.Run(context.Background(), []string{"--config", global, "locks", "mysql"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if !strings.Contains(stdout.String(), "mysql expired") {
		t.Fatalf("stdout = %q, want expired lock", stdout.String())
	}
}

func TestLocksNoneFound(t *testing.T) {
	root := t.TempDir()
	global, _ := writeLocksConfig(t, root)

	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	code := app.Run(context.Background(), []string{"--config", global, "locks", "mysql"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if !strings.Contains(stdout.String(), "no named runtime locks for mysql") {
		t.Fatalf("stdout = %q, want no-locks message", stdout.String())
	}
}

func TestLocksJSON(t *testing.T) {
	root := t.TempDir()
	global, locksDir := writeLocksConfig(t, root)
	writeLockFixture(t, locksDir, "mysql.lock", map[string]any{
		"service":           "mysql",
		"owner_pid":         os.Getpid(),
		"owner_start_ticks": selfStartTicks(t),
		"expires_at":        time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	})

	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
	code := app.Run(context.Background(), []string{"--config", global, "--json", "locks", "mysql"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}

	var got struct {
		Service string `json:"service"`
		Locks   []struct {
			State string `json:"state"`
		} `json:"locks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json: %v (out=%s)", err, stdout.String())
	}
	if got.Service != "mysql" || len(got.Locks) != 1 || got.Locks[0].State != "active" {
		t.Fatalf("unexpected JSON: %+v", got)
	}
}

func TestLocksRequiresService(t *testing.T) {
	var stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &bytes.Buffer{}, Stderr: &stderr}
	code := app.Run(context.Background(), []string{"locks"})
	if code != exitUsage {
		t.Fatalf("Run() exit = %d, want %d", code, exitUsage)
	}
}
