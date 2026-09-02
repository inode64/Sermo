package cli

import (
	"context"
	"fmt"
	"time"

	sermoapp "sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/state"
)

// monitorVerb returns the operator verb for a pause/resume transition.
func monitorVerb(pause bool) string {
	if pause {
		return commandUnmonitor
	}
	return commandMonitor
}

// runMonitor pauses (`unmonitor`) or resumes (`monitor`) monitoring of a service.
// A paused service keeps its config but the daemon runs no checks, rules or
// remediation for it until resumed. The state lives in the persistent store
// under paths.state (default /var/lib/sermo), so it survives daemon restarts and
// reboots — and a service whose `monitor` flag is `previous` is restored to it on
// the next daemon start.
func (a App) runMonitor(ctx context.Context, opts options, pause bool) int {
	verb := monitorVerb(pause)
	service := opts.service()
	if code := a.requireSingleServiceName(service != "", len(opts.args), verb, verb); code != exitSuccess {
		return code
	}

	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	service, code = a.canonicalService(opts, cfg, service)
	if code != exitSuccess {
		return code
	}

	return a.applyMonitorTransition(ctx, opts, cfg, service, verb, pause,
		func(err error) {
			a.recordAccess(cfg, verb, service, accessStatusError, err.Error())
		},
		func(store *state.Store, status string) int {
			a.recordAccess(cfg, verb, service, accessStatusOK, status)
			a.reportMonitor(opts, store, service, status)
			return exitSuccess
		},
	)
}

// applyMonitorTransition opens the store, applies pause/resume for key, and runs
// after on success. failPrefix labels open/update failures ("monitor failed: …").
// onUpdateErr is optional (service access-log on update failure).
func (a App) applyMonitorTransition(
	ctx context.Context,
	opts options,
	cfg *config.Config,
	key, failPrefix string,
	pause bool,
	onUpdateErr func(error),
	after func(store *state.Store, status string) int,
) int {
	return withStateStore(ctx, cfg, func(err error) int {
		return a.fail(opts, fmt.Sprintf("%s failed: %v", failPrefix, err))
	}, func(store *state.Store) int {
		status, err := updateMonitorState(store, key, pause)
		if err != nil {
			if onUpdateErr != nil {
				onUpdateErr(err)
			}
			return a.fail(opts, fmt.Sprintf("%s failed: %v", failPrefix, err))
		}
		return after(store, status)
	})
}

// updateMonitorState persists a monitor/unmonitor request and reports whether
// it paused an entry, resumed a paused entry, or found monitoring already on.
// Service and watch commands use independent keys but share these semantics.
func updateMonitorState(store *state.Store, key string, pause bool) (string, error) {
	transition, err := sermoapp.ApplyMonitorTransition(store, key, !pause, state.SourceCLI)
	if err != nil {
		return "", fmt.Errorf("apply monitor transition: %w", err)
	}
	if transition.Changed {
		if pause {
			return monitorStatusPaused, nil
		}
		return monitorStatusResumed, nil
	}
	if pause {
		return monitorStatusAlreadyPaused, nil
	}
	return monitorStatusNotPaused, nil
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
	case monitorStatusAlreadyPaused:
		fmt.Fprintf(a.Stdout, "monitoring already paused for %s%s\n", subject, suffix)
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
