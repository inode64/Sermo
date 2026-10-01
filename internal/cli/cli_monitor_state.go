package cli

import (
	"context"
	"strings"
	"time"

	"sermo/internal/app"
	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
)

// serviceDisplayState returns the operator-facing state for status output.
// When sermod is up it prefers the daemon's settled view (including starting);
// otherwise it derives state from the local backend query only.
func (a App) serviceDisplayState(ctx context.Context, cfg *config.Config, service string, status servicemgr.ServiceStatus, mon monitorView) string {
	if serviceState, ok := a.FetchDaemonServiceState(ctx, cfg, service); ok && serviceState != "" {
		return serviceState
	}
	return localServiceState(status, mon)
}

// localServiceState is the operator-facing state derived from the backend
// status alone, for when sermod does not answer. With no runtime samples at
// all it can never tell an empty process tree from one it has not sampled, so
// it never reports processes missing.
func localServiceState(status servicemgr.ServiceStatus, mon monitorView) string {
	return app.ServiceState(mon.Enabled, mon.Monitored(), string(status.Status), "", true, false, false, false, false)
}

// monitorView is the persisted monitoring metadata shown by status and monitor.
type monitorView struct {
	Configured bool
	Enabled    bool
	Paused     bool
	Source     string
	ChangedAt  string // RFC3339 when set
}

func (m monitorView) Monitored() bool {
	return m.Configured && m.Enabled && !m.Paused
}

// serviceMonitorState reads a service's monitoring row from the state store. It
// is best-effort: status works without config, so a missing config or store
// yields an empty view (not paused).
func serviceMonitorState(ctx context.Context, cfg *config.Config, service string, configured bool) monitorView {
	view := monitorView{Enabled: true}
	if cfg == nil {
		return view
	}
	if configured {
		var tree map[string]any
		if resolved, errs := cfg.Resolve(service); len(errs) == 0 {
			tree = resolved.Tree
		}
		view = configuredMonitorView(tree)
	}
	// The persisted monitor state only enriches the view; an unreadable store
	// leaves the configured view as it is.
	withStateStore(ctx, cfg, func(error) int { return exitSuccess }, func(store *state.Store) int {
		if record, found, err := store.MonitorState(service); err == nil && found {
			view = view.withRecord(record)
		}
		return exitSuccess
	})
	return view
}

// configuredMonitorView is the monitoring view a resolved service tree declares,
// before the persisted state is applied. A nil tree (resolution failed) keeps
// the service enabled and monitored.
func configuredMonitorView(tree map[string]any) monitorView {
	view := monitorView{Configured: true, Enabled: true}
	if cfgval.Disabled(tree) {
		view.Enabled = false
		view.Paused = true
	}
	if mode, _ := tree[config.EntryKeyMonitor].(string); mode == config.MonitorDisabled {
		view.Paused = true
	}
	return view
}

// storedMonitorStates reads every persisted monitor row in one query. The
// persisted state only enriches the configured view, so an unreadable store
// yields nil and each service keeps the monitoring its configuration declares.
func storedMonitorStates(ctx context.Context, cfg *config.Config) map[string]state.MonitorRecord {
	var stored map[string]state.MonitorRecord
	withStateStore(ctx, cfg, func(error) int { return exitSuccess }, func(store *state.Store) int {
		stored, _ = store.MonitorStates()
		return exitSuccess
	})
	return stored
}

// withRecord overlays one persisted monitor row.
func (m monitorView) withRecord(record state.MonitorRecord) monitorView {
	m.Paused = !record.Active
	m.Source = record.Source
	m.ChangedAt = recordChangedAt(record.UpdatedAt)
	return m
}

// metaSuffix renders the optional " source=… changed=…" trailer shared by the
// status line and the monitor pause/resume messages. Empty fields are omitted;
// an all-empty result is the empty string (no leading space).
func metaSuffix(source, changedAt string) string {
	var parts []string
	if source != "" {
		parts = append(parts, "source="+source)
	}
	if changedAt != "" {
		parts = append(parts, "changed="+changedAt)
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

// recordChangedAt renders persisted timestamps consistently, omitting zero values.
func recordChangedAt(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}
