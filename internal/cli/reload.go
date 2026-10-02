package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sermo/internal/hostfs"
	"strconv"
	"strings"
	"syscall"

	"sermo/internal/config"
	"sermo/internal/process"
	"sermo/internal/strutil"
)

func (a App) runServiceReload(ctx context.Context, opts options) int {
	if opts.service() == "" {
		return a.commandUsageError(commandReload, "reload requires a service name; use `sermoctl daemon reload` to reload sermod config")
	}
	return a.runAction(ctx, opts, commandReload)
}

// defaultReloadPidfileFallbacks are the absolute pidfiles `daemon reload` checks
// after the configured runtime dir. Keep this list restricted to current
// supported paths; old package locations are intentionally not searched.
func defaultReloadPidfileFallbacks() []string {
	return []string{filepath.Join(config.DefaultRuntime, daemonPIDFilename)}
}

func daemonReloadPidfileCandidates(primary string, fallbacks []string) []string {
	return strutil.Unique(append([]string{primary}, fallbacks...))
}

// runReload asks the running sermod to reload its configuration (SIGHUP
// equivalent). It prefers a pidfile written by the daemon under the configured
// runtime dir. If no pidfile is found it falls back to a native /proc scan for
// a running sermod process. This works whether or not the web UI is enabled.
func (a App) runReload(ctx context.Context, opts options) int {
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}

	runtimeDir := cfg.Global.RuntimeDir()

	fallbacks := a.pidfileFallbacks
	if fallbacks == nil {
		fallbacks = defaultReloadPidfileFallbacks()
	}
	candidates := daemonReloadPidfileCandidates(filepath.Join(runtimeDir, daemonPIDFilename), fallbacks)

	target, ok := a.findDaemon(candidates)
	if !ok {
		a.recordAccess(cfg, accessCommandDaemonReload, "", accessStatusError, "could not find running sermod pid")
		return a.fail(opts, "could not find running sermod pid (no pidfile naming a live sermod and no running sermod process)")
	}
	pid := target.PID

	signal := a.signalDaemon
	if signal == nil {
		signal = process.OSSignaler{}.SignalProcess
	}
	// The pidfd signaler revalidates target's start time and exe, so PID reuse
	// between the identity check and delivery cannot redirect SIGHUP.
	if err := signal(ctx, target, syscall.SIGHUP); err != nil {
		a.recordAccess(cfg, accessCommandDaemonReload, "", accessStatusError, err.Error())
		return a.fail(opts, fmt.Sprintf("failed to signal pid %d: %v", pid, err))
	}

	a.recordAccess(cfg, accessCommandDaemonReload, "", accessStatusOK, fmt.Sprintf("pid %d", pid))
	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyOK: true, cliJSONKeyPID: pid})
	} else {
		fmt.Fprintf(a.Stdout, "reload signal (HUP) sent to sermod pid %d\n", pid)
	}
	return exitSuccess
}

// findDaemon resolves the running sermod: pidfile candidates first, then a
// native /proc scan by program name. Every candidate PID must currently be a
// sermod process. A pidfile left behind by a SIGKILLed or OOM-killed daemon can
// name a recycled PID, and SIGHUP terminates most programs, so an unverified
// pidfile PID is skipped instead of signalled.
func (a App) findDaemon(pidfiles []string) (process.Process, bool) {
	identify := a.daemonIdentity
	if identify == nil {
		identify = process.OSReader{}.Identity
	}
	for _, p := range pidfiles {
		data, err := hostfs.ReadFile(p)
		if err != nil {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && n > 0 {
			if target, ok := daemonTarget(identify, n); ok {
				return target, true
			}
		}
	}
	// Fallback: find a running sermod by program name. This is a native /proc
	// scan (process.PIDsByComm), not a pidof/pgrep shell-out — it reads the
	// world-readable /proc/<pid>/comm so it locates a root-owned daemon without
	// external binaries.
	find := a.FindPID
	if find == nil {
		find = process.PIDsByComm
	}
	pids, err := find(daemonProcessName)
	if err != nil {
		return process.Process{}, false
	}
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if target, ok := daemonTarget(identify, pid); ok {
			return target, true
		}
	}
	return process.Process{}, false
}

// daemonTarget returns the signal target for pid when it is a live sermod.
// The executable basename is the proof because comm is writable by any process
// through prctl. A daemon whose binary a package upgrade replaced is still
// identified through its previous exe path so the operator gets the signaler's
// refusal (a deleted exe cannot authorize a reload) instead of "no sermod running".
func daemonTarget(identify func(int) (process.Identity, bool), pid int) (process.Process, bool) {
	id, ok := identify(pid)
	if !ok || !id.StartTicksOK {
		return process.Process{}, false
	}
	exe := id.Exe
	if !id.ExeOK {
		exe = id.ExePrev
	}
	if exe == "" || filepath.Base(exe) != daemonProcessName {
		return process.Process{}, false
	}
	return process.Process{PID: pid, StartTicks: id.StartTicks, Exe: id.Exe, ExeOK: id.ExeOK, UID: id.UID}, true
}
