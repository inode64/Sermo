package checks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writePid(t *testing.T, pid string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "svc.pid")
	if err := os.WriteFile(p, []byte(pid+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPidfileCheckRunningPasses(t *testing.T) {
	c := pidfileCheck{base: base{name: "pid", timeout: time.Second}, path: writePid(t, "4321"), alive: func(int) bool { return true }}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("a live pidfile must pass: %s", res.Message)
	}
	if res.Data["pid"] != 4321 {
		t.Fatalf("pid data = %v, want 4321", res.Data["pid"])
	}
}

func TestPidfileCheckMissingFails(t *testing.T) {
	c := pidfileCheck{base: base{name: "pid", timeout: time.Second}, path: filepath.Join(t.TempDir(), "absent.pid"), alive: func(int) bool { return true }}
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("a missing pidfile must fail")
	}
}

func TestPidfileCheckMissingPassesWithBackendFallback(t *testing.T) {
	c := pidfileCheck{
		base:         base{name: "pid", timeout: time.Second},
		path:         filepath.Join(t.TempDir(), "absent.pid"),
		alive:        func(pid int) bool { return pid == 4321 },
		fallbackPIDs: func() []int { return []int{0, 4321, 4321, 9999} },
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("a missing pidfile with live backend pid must pass: %s", res.Message)
	}
	pids, ok := res.Data["pids"].([]int)
	if !ok || len(pids) != 1 || pids[0] != 4321 {
		t.Fatalf("fallback pids = %#v, want [4321]", res.Data["pids"])
	}
}

func TestPidfileCheckStaleFails(t *testing.T) {
	c := pidfileCheck{base: base{name: "pid", timeout: time.Second}, path: writePid(t, "4321"), alive: func(int) bool { return false }}
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("a pidfile pointing at a dead pid must fail")
	}
}

func TestPidfileCheckDefaultAliveSelf(t *testing.T) {
	// With the default liveness probe, our own pid is alive and a huge pid is not.
	c := pidfileCheck{base: base{name: "pid", timeout: time.Second}, path: writePid(t, "1")}
	_ = c // pid 1 (init) always exists; just ensure default probe runs without panic
	live := pidAlive(os.Getpid())
	dead := pidAlive(1 << 30)
	if !live || dead {
		t.Fatalf("pidAlive self=%v huge=%v, want true/false", live, dead)
	}
}

func TestBuildPidfileCheckNeedsPath(t *testing.T) {
	if _, warn := buildPidfileCheck(base{}, map[string]any{}, Deps{}); warn == "" {
		t.Fatal("pidfile check without a path must warn")
	}
	if c, warn := buildPidfileCheck(base{}, map[string]any{"path": "/run/x.pid"}, Deps{}); warn != "" || c == nil {
		t.Fatalf("valid pidfile check should build: warn=%q", warn)
	}
}
