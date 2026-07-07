package checks

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	paths        []string
	alive        func(int) bool
	fallbackPIDs func() []int
}

func (c pidfileCheck) Run(_ context.Context) Result {
	start := time.Now()
	alive := c.alive
	if alive == nil {
		alive = pidAlive
	}
	if len(c.paths) == 0 {
		return c.result(false, "pidfile check has no path candidates", start)
	}
	var failures []string
	for _, path := range c.paths {
		pid, err := process.ReadPidfile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if !alive(pid) {
			failures = append(failures, fmt.Sprintf("%s references pid %d which is not running", path, pid))
			continue
		}
		r := c.result(true, fmt.Sprintf("%s -> pid %d running", path, pid), start)
		r.Data = map[string]any{DataKeyPID: pid, DataKeyPath: path}
		return r
	}
	if len(failures) > 0 {
		return c.result(false, strings.Join(failures, "; "), start)
	}
	if len(c.paths) == 1 {
		path := c.paths[0]
		if pids := c.liveFallbackPIDs(alive); len(pids) > 0 {
			r := c.result(true, fmt.Sprintf("%s absent; backend reports %d running pid(s)", path, len(pids)), start)
			r.Data = map[string]any{DataKeyPIDs: pids, DataKeySource: DataSourceBackend}
			return r
		}
		return c.result(false, path+" does not exist (service active but no pidfile)", start)
	}
	if pids := c.liveFallbackPIDs(alive); len(pids) > 0 {
		r := c.result(true, fmt.Sprintf("no pidfile candidate exists (%s); backend reports %d running pid(s)", strings.Join(c.paths, ", "), len(pids)), start)
		r.Data = map[string]any{DataKeyPIDs: pids, DataKeySource: DataSourceBackend, DataKeyPaths: c.paths}
		return r
	}
	return c.result(false, fmt.Sprintf("none of pidfile candidates exist (%s) (service active but no pidfile)", strings.Join(c.paths, ", ")), start)
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
