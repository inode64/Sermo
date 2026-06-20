package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePostMVPConfig(t *testing.T) (global, root string) {
	t.Helper()
	root = t.TempDir()
	global = filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  catalog: [ `+root+`/daemons ]
  services: [ `+root+`/enabled ]
  runtime: `+root+`/run
defaults:
  policy: { cooldown: 5m }
`)
	mustWrite(t, filepath.Join(root, "daemons", "redis.yml"), `
kind: daemon
name: redis
variables: { port: 6379 }
checks:
  tcp: { type: tcp, port: "${port}" }
`)
	mustWrite(t, filepath.Join(root, "enabled", "redis-main.yml"), `
kind: service
name: redis-main
uses: redis
`)
	mustWrite(t, filepath.Join(root, "enabled", "redis-alt.yml"), `
kind: service
name: redis-alt
uses: redis
variables: { port: 6380 }
`)
	return global, root
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &stderr}
	code := app.Run(context.Background(), args)
	return code, stdout.String(), stderr.String()
}

func TestLockAcquireListRelease(t *testing.T) {
	global, _ := writePostMVPConfig(t)

	code, out, _ := runCLI(t, "--config", global, "lock", "acquire", "mysql", "--name", "backup", "--reason", "nightly", "--ttl", "1h")
	if code != exitSuccess || !strings.Contains(out, "acquired") {
		t.Fatalf("lock acquire: code=%d out=%q", code, out)
	}

	// The locks command sees the named lock as active.
	code, out, _ = runCLI(t, "--config", global, "locks", "mysql")
	if code != exitSuccess || !strings.Contains(out, "mysql.backup active") {
		t.Fatalf("locks after acquire: code=%d out=%q", code, out)
	}

	code, out, _ = runCLI(t, "--config", global, "lock", "release", "mysql", "--name", "backup")
	if code != exitSuccess || !strings.Contains(out, "released mysql.backup") {
		t.Fatalf("lock release: code=%d out=%q", code, out)
	}

	code, out, _ = runCLI(t, "--config", global, "locks", "mysql")
	if code != exitSuccess || !strings.Contains(out, "no named runtime locks") {
		t.Fatalf("locks after release: code=%d out=%q", code, out)
	}
}

func TestLockAcquireRequiresReasonAndTTL(t *testing.T) {
	global, _ := writePostMVPConfig(t)
	code, _, stderr := runCLI(t, "--config", global, "lock", "acquire", "mysql", "--reason", "x")
	if code != exitUsage || !strings.Contains(stderr, "--ttl is required") {
		t.Fatalf("missing ttl: code=%d stderr=%q", code, stderr)
	}
}

func TestLockWrapHoldsDuringCommand(t *testing.T) {
	global, root := writePostMVPConfig(t)
	lockPath := filepath.Join(root, "run", "locks", "mysql.lock")

	// The wrapped command checks that the lock file exists while it runs.
	code, _, _ := runCLI(t, "--config", global, "lock", "mysql", "--reason", "work", "--ttl", "1h",
		"--", "sh", "-c", "[ -f "+lockPath+" ]")
	if code != exitSuccess {
		t.Fatalf("wrap exit = %d, want 0 (lock present during command)", code)
	}
	// The lock is released after the command exits.
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock should be released after wrapper: %v", err)
	}
}
