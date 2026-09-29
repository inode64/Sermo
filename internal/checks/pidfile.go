package checks

import (
	"context"
	"errors"
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
//
// Liveness alone cannot tell the daemon from an unrelated process that
// recycled its PID after it died, which is exactly the failure this check
// exists for. claim, when set, reports whether the service's own process
// selectors name the PID; known is false when they cannot judge it.
type pidfileCheck struct {
	base
	paths        []string
	alive        func(int) bool
	claim        func(pid int) (claimed, known bool)
	fallbackPIDs func() []int
}

func (c pidfileCheck) Run(_ context.Context) Result {
	start := time.Now()
	alive := c.alive
	if alive == nil {
		alive = pidAlive
	}
	match := firstPathMatch(c.paths, func(path string) pathMatch {
		pid, err := process.ReadPidfile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return pathMatch{missing: true}
			}
			return pathMatch{failure: fmt.Sprintf("%s: %v", path, err), unavailable: true}
		}
		if !alive(pid) {
			return pathMatch{failure: fmt.Sprintf("%s references pid %d which is not running", path, pid)}
		}
		if c.claim != nil {
			if claimed, known := c.claim(pid); known && !claimed {
				return pathMatch{failure: fmt.Sprintf("%s references pid %d which is running but is not a process of this service (pid reused?)", path, pid)}
			}
		}
		return pathMatch{message: fmt.Sprintf("%s -> pid %d running", path, pid), data: map[string]any{DataKeyPID: pid, DataKeyPath: path}}
	}, CheckTypePidfile)
	if match.failure != "" {
		if match.unavailable {
			return c.unavailableResult(match.failure, start)
		}
		return c.result(false, match.failure, start)
	}
	if !match.missing {
		result := c.result(true, match.message, start)
		result.Data = match.data
		return result
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

// pidAlive reports whether a process with the given PID exists and has not
// exited. Linux/Unix, matching the rest of the daemon.
func pidAlive(pid int) bool {
	return pidRunning(pid, pidExists, pidState)
}

// pidRunning rejects a zombie: it has exited and only waits for its parent to
// reap it, so a daemon that died that way is not running. An unreadable state
// keeps the existence answer, since /proc may be restricted for other users.
func pidRunning(pid int, exists func(int) bool, state func(int) (string, bool)) bool {
	if pid <= 0 || !exists(pid) {
		return false
	}
	st, ok := state(pid)
	return !ok || st != process.ProcStateZombie
}

// pidExists probes existence with signal 0, which does not affect the target;
// EPERM means it exists but is owned by another user (still alive).
func pidExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func pidState(pid int) (string, bool) {
	fields, ok := process.StatFields(pid)
	if !ok || len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}
