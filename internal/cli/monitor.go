package cli

import (
	"fmt"
	"path/filepath"
	"time"

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
		return a.usageError(fmt.Sprintf("%s requires a service name", verb))
	}

	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}
	if code := a.requireService(opts, cfg, service); code != exitSuccess {
		return code
	}

	store, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		return a.fail(opts, fmt.Sprintf("%s failed: %v", verb, err))
	}
	defer store.Close()

	if pause {
		if err := store.SetActive(service, false, state.SourceCLI); err != nil {
			return a.fail(opts, fmt.Sprintf("unmonitor failed: %v", err))
		}
		a.reportMonitor(opts, store, service, "paused")
		return exitSuccess
	}

	active, found, err := store.Active(service)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("monitor failed: %v", err))
	}
	if err := store.SetActive(service, true, state.SourceCLI); err != nil {
		return a.fail(opts, fmt.Sprintf("monitor failed: %v", err))
	}
	status := "resumed"
	if !found || active {
		status = "not-paused"
	}
	a.reportMonitor(opts, store, service, status)
	return exitSuccess
}

func (a App) reportMonitor(opts options, store *state.Store, service, status string) {
	rec, found, _ := store.MonitorState(service)
	payload := map[string]any{"service": service, "monitoring": status}
	if found {
		if rec.Source != "" {
			payload["monitor_source"] = rec.Source
		}
		if !rec.UpdatedAt.IsZero() {
			payload["monitor_changed_at"] = rec.UpdatedAt.UTC().Format(time.RFC3339)
		}
	}
	if opts.json {
		writeJSON(a.Stdout, payload)
		return
	}
	switch status {
	case "paused":
		fmt.Fprintf(a.Stdout, "monitoring paused for %s%s\n", service, monitorMetaSuffix(rec, found))
	case "resumed":
		fmt.Fprintf(a.Stdout, "monitoring resumed for %s%s\n", service, monitorMetaSuffix(rec, found))
	default:
		fmt.Fprintf(a.Stdout, "%s was not paused\n", service)
	}
}

func monitorMetaSuffix(rec state.MonitorRecord, found bool) string {
	if !found {
		return ""
	}
	suffix := ""
	if rec.Source != "" {
		suffix += " source=" + rec.Source
	}
	if !rec.UpdatedAt.IsZero() {
		suffix += " changed=" + rec.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return suffix
}
