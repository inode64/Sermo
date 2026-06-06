package cli

import (
	"fmt"
	"path/filepath"

	"sermo/internal/state"
)

// runMonitor pauses (`unmonitor`) or resumes (`monitor`) monitoring of a service.
// A paused service keeps its config but the daemon runs no checks, rules or
// remediation for it until resumed. The state lives in the persistent store
// under paths.state (default /var/lib/sermo), so it survives daemon restarts and
// reboots — and a service whose `monitor` flag is `previous` is restored to it on
// the next daemon start.
func (a App) runMonitor(opts options, pause bool) int {
	verb := "monitor"
	if pause {
		verb = "unmonitor"
	}
	service := opts.service()
	if service == "" {
		fmt.Fprintf(a.Stderr, "usage error: %s requires a service name\n", verb)
		return exitUsage
	}

	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}
	if _, ok := cfg.Services[service]; !ok {
		a.reportError(opts, fmt.Sprintf("unknown service %q", service))
		return exitRuntimeError
	}

	store, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		a.reportError(opts, fmt.Sprintf("%s failed: %v", verb, err))
		return exitRuntimeError
	}
	defer store.Close()

	if pause {
		if err := store.SetActive(service, false, state.SourceCLI); err != nil {
			a.reportError(opts, fmt.Sprintf("unmonitor failed: %v", err))
			return exitRuntimeError
		}
		a.reportMonitor(opts, service, "paused")
		return exitSuccess
	}

	active, found, err := store.Active(service)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("monitor failed: %v", err))
		return exitRuntimeError
	}
	if err := store.SetActive(service, true, state.SourceCLI); err != nil {
		a.reportError(opts, fmt.Sprintf("monitor failed: %v", err))
		return exitRuntimeError
	}
	status := "resumed"
	if !found || active {
		status = "not-paused"
	}
	a.reportMonitor(opts, service, status)
	return exitSuccess
}

func (a App) reportMonitor(opts options, service, status string) {
	if opts.json {
		writeJSON(a.Stdout, map[string]any{"service": service, "monitoring": status})
		return
	}
	switch status {
	case "paused":
		fmt.Fprintf(a.Stdout, "monitoring paused for %s\n", service)
	case "resumed":
		fmt.Fprintf(a.Stdout, "monitoring resumed for %s\n", service)
	default:
		fmt.Fprintf(a.Stdout, "%s was not paused\n", service)
	}
}
