package cli

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/state"
)

// runMonitor pauses (`unmonitor`) or resumes (`monitor`) monitoring of a service.
// A paused service keeps its config but the daemon runs no checks, rules or
// remediation for it until resumed. The state lives in the persistent store
// under paths.state (default /var/lib/sermo), so it survives daemon restarts and
// reboots — and a service whose `monitor` flag is `previous` is restored to it on
// the next daemon start.
func (a App) runMonitor(ctx context.Context, opts options, pause bool) int {
	verb := commandMonitor
	if pause {
		verb = commandUnmonitor
	}
	service := opts.service()
	if service == "" {
		return a.commandUsageError(verb, verb+" requires a service name")
	}
	if len(opts.args) > 1 {
		return a.commandUsageError(verb, verb+" takes exactly one service name")
	}

	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}
	service, code = a.canonicalService(opts, cfg, service)
	if code != exitSuccess {
		return code
	}

	store, err := openStateStore(ctx, cfg)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("%s failed: %v", verb, err))
	}
	defer store.Close()

	status, err := updateMonitorState(store, service, pause)
	if err != nil {
		a.recordAccess(cfg, verb, service, accessStatusError, err.Error())
		return a.fail(opts, fmt.Sprintf("%s failed: %v", verb, err))
	}
	a.recordAccess(cfg, verb, service, accessStatusOK, status)
	a.reportMonitor(opts, store, service, status)
	return exitSuccess
}

// updateMonitorState persists a monitor/unmonitor request and reports whether
// it paused an entry, resumed a paused entry, or found monitoring already on.
// Service and watch commands use independent keys but share these semantics.
func updateMonitorState(store *state.Store, key string, pause bool) (string, error) {
	if pause {
		if err := store.SetActive(key, false, state.SourceCLI); err != nil {
			return "", fmt.Errorf("pause monitoring: %w", err)
		}
		return monitorStatusPaused, nil
	}

	active, found, err := store.Active(key)
	if err != nil {
		return "", fmt.Errorf("read monitoring state: %w", err)
	}
	if err := store.SetActive(key, true, state.SourceCLI); err != nil {
		return "", fmt.Errorf("resume monitoring: %w", err)
	}
	if !found || active {
		return monitorStatusNotPaused, nil
	}
	return monitorStatusResumed, nil
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
	a.printMonitorStatus(service, status, monitorMetaSuffix(rec, found))
}

// printMonitorStatus prints the human-readable monitor transition for subject
// ("web" or "watch storage-root"); suffix carries optional source/changed meta.
func (a App) printMonitorStatus(subject, status, suffix string) {
	switch status {
	case monitorStatusPaused:
		fmt.Fprintf(a.Stdout, "monitoring paused for %s%s\n", subject, suffix)
	case monitorStatusResumed:
		fmt.Fprintf(a.Stdout, "monitoring resumed for %s%s\n", subject, suffix)
	default:
		fmt.Fprintf(a.Stdout, "%s was not paused\n", subject)
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
