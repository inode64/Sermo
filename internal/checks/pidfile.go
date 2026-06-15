package checks

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	"sermo/internal/process"
)

// pidfileCheck passes when the pidfile exists and references a running process.
// It is meant to be gated with `requires: [service]` so a stopped service (whose
// pidfile is legitimately absent) is skipped, and a *missing or stale* pidfile is
// an error only while the service is active — which means the daemon died or lost
// its pidfile without the service manager noticing. The `alive` probe is
// injectable for tests; it defaults to a /proc-or-signal liveness check.
type pidfileCheck struct {
	base
	path         string
	alive        func(int) bool
	fallbackPIDs func() []int
}

func (c pidfileCheck) Run(_ context.Context) Result {
	start := time.Now()
	alive := c.alive
	if alive == nil {
		alive = pidAlive
	}
	pid, err := process.ReadPidfile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			if pids := c.liveFallbackPIDs(alive); len(pids) > 0 {
				r := c.result(true, fmt.Sprintf("%s absent; backend reports %d running pid(s)", c.path, len(pids)), start)
				r.Data = map[string]any{"pids": pids, "source": "backend"}
				return r
			}
			return c.result(false, c.path+" does not exist (service active but no pidfile)", start)
		}
		return c.result(false, fmt.Sprintf("%s: %v", c.path, err), start)
	}
	if !alive(pid) {
		r := c.result(false, fmt.Sprintf("%s references pid %d which is not running", c.path, pid), start)
		r.Data = map[string]any{"pid": pid}
		return r
	}
	r := c.result(true, fmt.Sprintf("%s -> pid %d running", c.path, pid), start)
	r.Data = map[string]any{"pid": pid}
	return r
}

func (c pidfileCheck) liveFallbackPIDs(alive func(int) bool) []int {
	if c.fallbackPIDs == nil {
		return nil
	}
	seen := map[int]bool{}
	var out []int
	for _, pid := range c.fallbackPIDs() {
		if pid <= 0 || seen[pid] || !alive(pid) {
			continue
		}
		seen[pid] = true
		out = append(out, pid)
	}
	return out
}

// pidAlive reports whether a process with the given PID exists. Signal 0 probes
// existence without affecting the target; EPERM means it exists but is owned by
// another user (still alive). Linux/Unix, matching the rest of the daemon.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
