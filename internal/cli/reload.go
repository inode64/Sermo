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
func (a App) runReload(_ context.Context, opts options) int {
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}

	runtimeDir := cfg.Global.RuntimeDir()
	if runtimeDir == "" {
		runtimeDir = config.DefaultRuntime
	}

	fallbacks := a.pidfileFallbacks
	if fallbacks == nil {
		fallbacks = defaultReloadPidfileFallbacks()
	}
	candidates := daemonReloadPidfileCandidates(filepath.Join(runtimeDir, daemonPIDFilename), fallbacks)

	var pid int
	for _, p := range candidates {
		data, err := hostfs.ReadFile(p)
		if err != nil {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && n > 0 {
			pid = n
			break
		}
	}

	if pid == 0 {
		// Fallback: find a running sermod by program name. This is a native
		// /proc scan (process.PIDsByComm), not a pidof/pgrep shell-out — it
		// reads the world-readable /proc/<pid>/comm so it locates a root-owned
		// daemon without external binaries.
		find := a.FindPID
		if find == nil {
			find = process.PIDsByComm
		}
		if pids, err := find(daemonProcessName); err == nil {
			for _, p := range pids {
				if p > 0 {
					pid = p
					break
				}
			}
		}
	}

	if pid <= 0 {
		a.recordAccess(cfg, accessCommandDaemonReload, "", accessStatusError, "could not find running sermod pid")
		return a.fail(opts, "could not find running sermod pid (no pidfile and no running sermod process)")
	}

	// Send SIGHUP. On Linux this is reliable for the daemon's signal handler.
	if err := (process.OSSignaler{}).Signal(pid, syscall.SIGHUP); err != nil {
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
