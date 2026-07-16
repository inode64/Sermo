package cli

import (
	"context"
	"strings"
	"time"

	"sermo/internal/app"
	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/servicemgr"
)

// serviceDisplayState returns the operator-facing state for status output.
// When sermod is up it prefers the daemon's settled view (including starting);
// otherwise it derives state from the local backend query only.
func (a App) serviceDisplayState(ctx context.Context, opts options, status servicemgr.ServiceStatus, mon monitorView) string {
	if a.FetchDaemonServiceState != nil {
		service := opts.service()
		if service == "" {
			service = status.Service
		}
		if serviceState, ok := a.FetchDaemonServiceState(ctx, opts, service); ok && serviceState != "" {
			return serviceState
		}
	}
	return app.ServiceState(mon.Enabled, mon.Monitored(), string(status.Status), "", true, false)
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

// serviceMonitorState reads a service's monitoring row from the state store. It is
// best-effort: status works without config, so a missing config or store yields
// an empty view (not paused).
func (a App) serviceMonitorState(ctx context.Context, opts options) monitorView {
	view := monitorView{Enabled: true}
	cfg, err := a.LoadConfig(opts.globalPath())
	if err != nil {
		return view
	}
	service := opts.service()
	if canonical, ok := cfg.CanonicalServiceName(service); ok {
		service = canonical
		view.Configured = true
		if resolved, errs := cfg.Resolve(service); len(errs) == 0 {
			if cfgval.Disabled(resolved.Tree) {
				view.Enabled = false
				view.Paused = true
			}
			if mode, _ := resolved.Tree[config.EntryKeyMonitor].(string); mode == config.MonitorDisabled {
				view.Paused = true
			}
		}
	}
	store, err := openStateStore(ctx, cfg)
	if err != nil {
		return view
	}
	defer store.Close()
	record, found, err := store.MonitorState(service) //nolint:contextcheck // ctx bound via OpenContext above
	if err != nil || !found {
		return view
	}
	view.Paused = !record.Active
	view.Source = record.Source
	if !record.UpdatedAt.IsZero() {
		view.ChangedAt = record.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return view
}

func formatStateMetadata(mon monitorView) string {
	return metaSuffix(mon.Source, mon.ChangedAt)
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
