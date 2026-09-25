package cli

import (
	"context"
	"errors"
	"fmt"

	"sermo/internal/config"
	"sermo/internal/control"
	"sermo/internal/servicemgr"
)

func (a App) runBackend(ctx context.Context, opts options) int {
	if len(opts.args) > 0 {
		return a.commandUsageError(opts.command, opts.command+" takes no arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	backend, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("backend detection failed: %v", err))
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]string{cliJSONKeyBackend: string(backend)})
		return exitSuccess
	}

	fmt.Fprintln(a.Stdout, backend)
	return exitSuccess
}

func (a App) runStatus(ctx context.Context, opts options) int {
	if code := a.requireSingleServiceName(opts.service() != "", len(opts.args), commandStatus, commandStatus); code != exitSuccess {
		return code
	}

	cfg := a.statusConfig(opts)
	status, service, configured, code := a.serviceStatus(ctx, opts, cfg)
	if code != exitSuccess {
		return code
	}

	mon := serviceMonitorState(ctx, cfg, service, configured)
	displayState := a.serviceDisplayState(ctx, cfg, service, status, mon)
	if opts.json {
		writeJSON(a.Stdout, statusToJSON(status, mon, displayState))
		return exitSuccess
	}

	fmt.Fprintf(a.Stdout, "%s state=%s backend=%s service=%s%s\n",
		status.Service, displayState, status.Backend, status.Unit, metaSuffix(mon.Source, mon.ChangedAt))
	return exitSuccess
}

func (a App) runIsActive(ctx context.Context, opts options) int {
	if code := a.requireSingleServiceName(opts.service() != "", len(opts.args), commandIsActive, commandIsActive); code != exitSuccess {
		return code
	}

	cfg := a.statusConfig(opts)
	status, service, configured, code := a.serviceStatus(ctx, opts, cfg)
	if code != exitSuccess {
		return code
	}

	switch {
	case opts.json:
		mon := serviceMonitorState(ctx, cfg, service, configured)
		writeJSON(a.Stdout, statusToJSON(status, mon, a.serviceDisplayState(ctx, cfg, service, status, mon)))
	case !opts.quiet:
		fmt.Fprintln(a.Stdout, status.Status)
	}

	if status.Status == servicemgr.StatusActive {
		return exitSuccess
	}
	return exitNotActive
}

// serviceStatus resolves the backend, builds a manager and queries the service.
// On any failure it reports the error and returns a non-success exit code.
func (a App) serviceStatus(ctx context.Context, opts options, cfg *config.Config) (servicemgr.ServiceStatus, string, bool, int) {
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	dependencies, err := a.controlDependenciesFor(ctx, opts.backend)
	if err != nil {
		a.reportError(opts, err.Error())
		return servicemgr.ServiceStatus{}, "", false, exitRuntimeError
	}

	service := opts.service()
	configured := false
	// Only Unit and Manager are read below; the config branch replaces the whole
	// target when it resolves one, so setting Backend here would never be seen.
	target := control.Target{Unit: service, Manager: dependencies.manager}
	if cfg != nil {
		if canonical, ok := cfg.CanonicalServiceName(service); ok {
			configured = true
			service = canonical
			resolved, errs := cfg.Resolve(service)
			if len(errs) > 0 {
				a.reportError(opts, fmt.Sprintf("config resolve failed: %v", errs[0]))
				return servicemgr.ServiceStatus{}, "", false, exitRuntimeError
			}
			target, err = a.resolveControlTarget(ctx, opts, service, resolved.Tree, dependencies.backend, dependencies.manager, dependencies.resolver)
			if err != nil {
				a.reportError(opts, fmt.Sprintf("control target failed: %v", err))
				return servicemgr.ServiceStatus{}, "", false, exitRuntimeError
			}
		} else if len(cfg.Services) > 0 {
			a.reportError(opts, fmt.Sprintf(cliUnknownServiceFormat, service))
			return servicemgr.ServiceStatus{}, "", false, exitRuntimeError
		}
	}

	status, err := target.Manager.Status(ctx, target.Unit)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("status query failed: %v", err))
		return servicemgr.ServiceStatus{}, "", false, exitRuntimeError
	}
	return status, service, configured, exitSuccess
}

// statusConfig loads the optional config once. Status still works for a direct
// service unit when the config is absent or invalid.
func (a App) statusConfig(opts options) *config.Config {
	cfg, err := a.LoadConfig(opts.globalPath())
	if err != nil {
		return nil
	}
	return cfg
}

func (a App) resolveControlTarget(ctx context.Context, opts options, service string, tree map[string]any, backend servicemgr.Backend, manager servicemgr.Manager, resolver servicemgr.UnitResolver) (control.Target, error) {
	target, warning := control.ResolveWithFallback(ctx, service, tree, backend, manager, resolver)
	if warning == "" {
		return target, nil
	}
	if target.Unit == "" {
		return control.Target{}, errors.New(warning)
	}
	if !opts.quiet {
		fmt.Fprintf(a.Stderr, "warning: service %s: %s\n", service, warning)
	}
	return target, nil
}

type statusJSON struct {
	Service          string `json:"service"`
	State            string `json:"state"`
	Backend          string `json:"backend"`
	Status           string `json:"status"`
	Unit             string `json:"unit"`
	Paused           bool   `json:"paused"`
	MonitorSource    string `json:"monitor_source,omitempty"`
	MonitorChangedAt string `json:"monitor_changed_at,omitempty"`
}

func statusToJSON(status servicemgr.ServiceStatus, mon monitorView, displayState string) statusJSON {
	out := statusJSON{
		Service: status.Service,
		State:   displayState,
		Backend: string(status.Backend),
		Status:  string(status.Status),
		Unit:    status.Unit,
		Paused:  mon.Paused,
	}
	if mon.Paused {
		out.MonitorSource = mon.Source
		out.MonitorChangedAt = mon.ChangedAt
	}
	return out
}
