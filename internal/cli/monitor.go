package cli

import (
	"fmt"
	"path/filepath"

	"sermo/internal/locks"
)

// runMonitor pauses (`unmonitor`) or resumes (`monitor`) monitoring of a service.
// A paused service keeps its config but the daemon runs no checks, rules or
// remediation for it until resumed. The state is a marker file under
// <runtime>/paused, so it survives daemon restarts.
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

	store := locks.NewPauseStore(filepath.Join(cfg.Global.RuntimeDir(), "paused"))

	if pause {
		path, err := store.Pause(service)
		if err != nil {
			a.reportError(opts, fmt.Sprintf("unmonitor failed: %v", err))
			return exitRuntimeError
		}
		a.reportMonitor(opts, service, "paused", path)
		return exitSuccess
	}

	was, err := store.Resume(service)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("monitor failed: %v", err))
		return exitRuntimeError
	}
	state := "resumed"
	if !was {
		state = "not-paused"
	}
	a.reportMonitor(opts, service, state, "")
	return exitSuccess
}

func (a App) reportMonitor(opts options, service, state, path string) {
	if opts.json {
		out := map[string]any{"service": service, "monitoring": state}
		if path != "" {
			out["marker"] = path
		}
		writeJSON(a.Stdout, out)
		return
	}
	switch state {
	case "paused":
		fmt.Fprintf(a.Stdout, "monitoring paused for %s\n", service)
	case "resumed":
		fmt.Fprintf(a.Stdout, "monitoring resumed for %s\n", service)
	default:
		fmt.Fprintf(a.Stdout, "%s was not paused\n", service)
	}
}
