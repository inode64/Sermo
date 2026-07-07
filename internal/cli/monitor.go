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
	verb := commandMonitor
	if pause {
		verb = commandUnmonitor
	}
	service := opts.service()
	if service == "" {
		return a.commandUsageError(verb, fmt.Sprintf("%s requires a service name", verb))
	}
	if len(opts.args) > 1 {
		return a.commandUsageError(verb, fmt.Sprintf("%s takes exactly one service name", verb))
	}

	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}
	service, code = a.canonicalService(opts, cfg, service)
	if code != exitSuccess {
		return code
	}

	store, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		return a.fail(opts, fmt.Sprintf("%s failed: %v", verb, err))
	}
	defer store.Close()

	if pause {
		if err := store.SetActive(service, false, state.SourceCLI); err != nil {
			a.recordAccess(cfg, verb, service, accessStatusError, err.Error())
			return a.fail(opts, fmt.Sprintf("unmonitor failed: %v", err))
		}
		a.recordAccess(cfg, verb, service, accessStatusOK, monitorStatusPaused)
		a.reportMonitor(opts, store, service, monitorStatusPaused)
		return exitSuccess
	}

	active, found, err := store.Active(service)
	if err != nil {
		a.recordAccess(cfg, verb, service, accessStatusError, err.Error())
		return a.fail(opts, fmt.Sprintf("monitor failed: %v", err))
	}
	if err := store.SetActive(service, true, state.SourceCLI); err != nil {
		a.recordAccess(cfg, verb, service, accessStatusError, err.Error())
		return a.fail(opts, fmt.Sprintf("monitor failed: %v", err))
	}
	status := monitorStatusResumed
	if !found || active {
		status = monitorStatusNotPaused
	}
	a.recordAccess(cfg, verb, service, accessStatusOK, status)
	a.reportMonitor(opts, store, service, status)
	return exitSuccess
}

func (a App) reportMonitor(opts options, store *state.Store, service, status string) {
	rec, found, _ := store.MonitorState(service)
	payload := map[string]any{cliJSONKeyService: service, cliJSONKeyMonitoring: status}
	if found {
		if rec.Source != "" {
			payload[cliJSONKeyMonitorSource] = rec.Source
		}
		if !rec.UpdatedAt.IsZero() {
			payload[cliJSONKeyMonitorChanged] = rec.UpdatedAt.UTC().Format(time.RFC3339)
		}
	}
	if opts.json {
		writeJSON(a.Stdout, payload)
		return
	}
	switch status {
	case monitorStatusPaused:
		fmt.Fprintf(a.Stdout, "monitoring paused for %s%s\n", service, monitorMetaSuffix(rec, found))
	case monitorStatusResumed:
		fmt.Fprintf(a.Stdout, "monitoring resumed for %s%s\n", service, monitorMetaSuffix(rec, found))
	default:
		fmt.Fprintf(a.Stdout, "%s was not paused\n", service)
	}
}

func monitorMetaSuffix(rec state.MonitorRecord, found bool) string {
	if !found {
		return ""
	}
	changedAt := ""
	if !rec.UpdatedAt.IsZero() {
		changedAt = rec.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return metaSuffix(rec.Source, changedAt)
}
