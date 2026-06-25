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
	c := pidfileCheck{base: base{name: "pid", timeout: time.Second}, paths: []string{writePid(t, "4321")}, alive: func(int) bool { return true }}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("a live pidfile must pass: %s", res.Message)
	}
	if res.Data["pid"] != 4321 {
		t.Fatalf("pid data = %v, want 4321", res.Data["pid"])
	}
}

func TestPidfileCheckMissingFails(t *testing.T) {
	c := pidfileCheck{base: base{name: "pid", timeout: time.Second}, paths: []string{filepath.Join(t.TempDir(), "absent.pid")}, alive: func(int) bool { return true }}
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("a missing pidfile must fail")
	}
}

func TestPidfileCheckCandidateListUsesFirstLivePidfile(t *testing.T) {
	c := pidfileCheck{
		base:  base{name: "pid", timeout: time.Second},
		paths: []string{filepath.Join(t.TempDir(), "absent.pid"), writePid(t, "4321")},
		alive: func(pid int) bool { return pid == 4321 },
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("a live candidate pidfile must pass: %s", res.Message)
	}
	if res.Data["pid"] != 4321 {
		t.Fatalf("pid data = %v, want 4321", res.Data["pid"])
	}
}

func TestPidfileCheckMissingPassesWithBackendFallback(t *testing.T) {
	c := pidfileCheck{
		base:         base{name: "pid", timeout: time.Second},
		paths:        []string{filepath.Join(t.TempDir(), "absent.pid")},
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
	c := pidfileCheck{base: base{name: "pid", timeout: time.Second}, paths: []string{writePid(t, "4321")}, alive: func(int) bool { return false }}
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("a pidfile pointing at a dead pid must fail")
	}
}

func TestPidfileCheckDefaultAliveSelf(t *testing.T) {
	// With the default liveness probe, our own pid is alive and a huge pid is not.
	c := pidfileCheck{base: base{name: "pid", timeout: time.Second}, paths: []string{writePid(t, "1")}}
	_ = c // pid 1 (init) always exists; just ensure default probe runs without panic
	live := pidAlive(os.Getpid())
	dead := pidAlive(1 << 30)
	if !live || dead {
		t.Fatalf("pidAlive self=%v huge=%v, want true/false", live, dead)
	}
	// pid <= 0 is never a real process (and 0 would signal the whole group):
	// pidAlive must reject it without probing.
	if pidAlive(0) || pidAlive(-1) {
		t.Fatalf("pidAlive(0)=%v pidAlive(-1)=%v, want both false", pidAlive(0), pidAlive(-1))
	}
}

func TestBuildPidfileCheckNeedsPath(t *testing.T) {
	if _, warn := buildPidfileCheck(base{}, map[string]any{}, Deps{}); warn == "" {
		t.Fatal("pidfile check without a path must warn")
	}
	if c, warn := buildPidfileCheck(base{}, map[string]any{"path": "/run/x.pid"}, Deps{}); warn != "" || c == nil {
		t.Fatalf("valid pidfile check should build: warn=%q", warn)
	}
	if c, warn := buildPidfileCheck(base{}, map[string]any{"path": []any{"/run/a.pid", "/run/b.pid"}}, Deps{}); warn != "" || c == nil {
		t.Fatalf("valid pidfile candidate list should build: warn=%q", warn)
	}
}

func TestPidfileLiveFallbackPIDsFiltersNonPositiveAndDupes(t *testing.T) {
	c := pidfileCheck{fallbackPIDs: func() []int { return []int{0, -3, 5, 5, 7} }}
	got := c.liveFallbackPIDs(func(int) bool { return true })
	// pid <= 0 are dropped, duplicates collapsed: [5, 7].
	if len(got) != 2 || got[0] != 5 || got[1] != 7 {
		t.Fatalf("liveFallbackPIDs = %v, want [5 7]", got)
	}
}
