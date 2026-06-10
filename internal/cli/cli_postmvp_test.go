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
  profiles: [ `+root+`/profiles ]
  includes: [ `+root+`/enabled ]
  runtime: `+root+`/run
defaults:
  policy: { cooldown: 5m }
`)
	mustWrite(t, filepath.Join(root, "profiles", "redis.yml"), `
kind: profile
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

func TestProfileListAndShow(t *testing.T) {
	global, _ := writePostMVPConfig(t)

	code, out, _ := runCLI(t, "--config", global, "profile", "list")
	if code != exitSuccess || strings.TrimSpace(out) != "redis" {
		t.Fatalf("profile list: code=%d out=%q", code, out)
	}

	code, out, _ = runCLI(t, "--config", global, "profile", "show", "redis")
	if code != exitSuccess || !strings.Contains(out, "name: redis") {
		t.Fatalf("profile show: code=%d out=%q", code, out)
	}

	code, _, stderr := runCLI(t, "--config", global, "profile", "show", "nope")
	if code != exitRuntimeError || !strings.Contains(stderr, "unknown profile") {
		t.Fatalf("profile show nope: code=%d stderr=%q", code, stderr)
	}
}

func TestServiceListAndShow(t *testing.T) {
	global, _ := writePostMVPConfig(t)

	code, out, _ := runCLI(t, "--config", global, "service", "list")
	if code != exitSuccess {
		t.Fatalf("service list code=%d", code)
	}
	if !strings.Contains(out, "redis-main") || !strings.Contains(out, "redis-alt") {
		t.Fatalf("service list out=%q", out)
	}

	code, out, _ = runCLI(t, "--config", global, "service", "show", "redis-main")
	if code != exitSuccess || !strings.Contains(out, "port: \"6379\"") {
		t.Fatalf("service show: code=%d out=%q", code, out)
	}
}

func TestServiceClone(t *testing.T) {
	global, root := writePostMVPConfig(t)

	code, out, _ := runCLI(t, "--config", global, "service", "clone", "redis-main", "redis-clone")
	if code != exitSuccess {
		t.Fatalf("service clone code=%d out=%q", code, out)
	}
	path := filepath.Join(root, "enabled", "redis-clone.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("clone file not written: %v", err)
	}
	if !strings.Contains(string(data), "clone: redis-main") || !strings.Contains(string(data), "name: redis-clone") {
		t.Fatalf("clone content = %q", data)
	}

	// Cloning onto an existing service fails.
	code, _, stderr := runCLI(t, "--config", global, "service", "clone", "redis-main", "redis-alt")
	if code != exitRuntimeError || !strings.Contains(stderr, "already exists") {
		t.Fatalf("duplicate clone: code=%d stderr=%q", code, stderr)
	}
}

func TestConfigDiff(t *testing.T) {
	global, _ := writePostMVPConfig(t)

	code, out, _ := runCLI(t, "--config", global, "config", "diff", "redis-main", "redis-alt")
	if code != exitSuccess {
		t.Fatalf("config diff code=%d", code)
	}
	// redis-main resolves port 6379, redis-alt 6380.
	if !strings.Contains(out, `- `) || !strings.Contains(out, `+ `) {
		t.Fatalf("diff should show changed lines:\n%s", out)
	}
	if !strings.Contains(out, "6379") || !strings.Contains(out, "6380") {
		t.Fatalf("diff should mention both ports:\n%s", out)
	}
}

func TestConfigDiffIdentical(t *testing.T) {
	global, _ := writePostMVPConfig(t)

	code, out, _ := runCLI(t, "--config", global, "config", "diff", "redis-main", "redis-main")
	if code != exitSuccess {
		t.Fatalf("config diff identical code=%d", code)
	}
	if !strings.Contains(out, "resolve identically") {
		t.Fatalf("identical diff out=%q", out)
	}
}

func TestConfigDiffJSON(t *testing.T) {
	global, _ := writePostMVPConfig(t)

	code, out, _ := runCLI(t, "--config", global, "--json", "config", "diff", "redis-main", "redis-alt")
	if code != exitSuccess {
		t.Fatalf("config diff json code=%d", code)
	}
	if !strings.Contains(out, `"identical":false`) {
		t.Fatalf("json should report not identical:\n%s", out)
	}
	if !strings.Contains(out, `"removed"`) || !strings.Contains(out, `"added"`) {
		t.Fatalf("json should include removed and added:\n%s", out)
	}
	if !strings.Contains(out, "6379") || !strings.Contains(out, "6380") {
		t.Fatalf("json should mention both ports:\n%s", out)
	}
}

func TestConfigDiffUsage(t *testing.T) {
	global, _ := writePostMVPConfig(t)

	code, _, stderr := runCLI(t, "--config", global, "config", "diff", "redis-main")
	if code != exitUsage || !strings.Contains(stderr, "requires BASE and SERVICE") {
		t.Fatalf("diff usage: code=%d stderr=%q", code, stderr)
	}
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
