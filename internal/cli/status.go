package cli

import (
	"context"
	"errors"
	"fmt"

	"sermo/internal/control"
	"sermo/internal/servicemgr"
)

func (a App) runBackend(ctx context.Context, opts options) int {
	if len(opts.args) > 0 {
		return a.commandUsageError(opts.command, opts.command+" takes no arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		if opts.json {
			writeJSON(a.Stdout, map[string]string{cliJSONKeyError: err.Error()})
		} else {
			fmt.Fprintf(a.Stderr, "backend detection failed: %v\n", err)
		}
		return exitRuntimeError
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]string{cliJSONKeyBackend: string(detection.Backend)})
		return exitSuccess
	}

	fmt.Fprintln(a.Stdout, detection.Backend)
	return exitSuccess
}

func (a App) runStatus(ctx context.Context, opts options) int {
	if code := a.requireSingleServiceName(opts.service() != "", len(opts.args), commandStatus, commandStatus); code != exitSuccess {
		return code
	}

	status, code := a.serviceStatus(ctx, opts)
	if code != exitSuccess {
		return code
	}

	mon := a.serviceMonitorState(ctx, opts)
	displayState := a.serviceDisplayState(ctx, opts, status, mon)
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

	status, code := a.serviceStatus(ctx, opts)
	if code != exitSuccess {
		return code
	}

	switch {
	case opts.json:
		mon := a.serviceMonitorState(ctx, opts)
		writeJSON(a.Stdout, statusToJSON(status, mon, a.serviceDisplayState(ctx, opts, status, mon)))
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
func (a App) serviceStatus(ctx context.Context, opts options) (servicemgr.ServiceStatus, int) {
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("backend detection failed: %v", err))
		return servicemgr.ServiceStatus{}, exitRuntimeError
	}

	manager, err := a.NewManager(detection.Backend)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("service manager unavailable: %v", err))
		return servicemgr.ServiceStatus{}, exitRuntimeError
	}

	service := opts.service()
	// Only Unit and Manager are read below; the config branch replaces the whole
	// target when it resolves one, so setting Backend here would never be seen.
	target := control.Target{Unit: service, Manager: manager}
	if cfg, err := a.LoadConfig(opts.globalPath()); err == nil {
		if canonical, ok := cfg.CanonicalServiceName(service); ok {
			service = canonical
			resolved, errs := cfg.Resolve(service)
			if len(errs) > 0 {
				a.reportError(opts, fmt.Sprintf("config resolve failed: %v", errs[0]))
				return servicemgr.ServiceStatus{}, exitRuntimeError
			}
			resolver := servicemgr.NewUnitResolver()
			resolver.Runner = a.Runner
			resolver.Manager = manager
			target, err = a.resolveControlTarget(ctx, opts, service, resolved.Tree, detection.Backend, manager, resolver)
			if err != nil {
				a.reportError(opts, fmt.Sprintf("control target failed: %v", err))
				return servicemgr.ServiceStatus{}, exitRuntimeError
			}
		} else if len(cfg.Services) > 0 {
			a.reportError(opts, fmt.Sprintf(cliUnknownServiceFormat, service))
			return servicemgr.ServiceStatus{}, exitRuntimeError
		}
	}

	status, err := target.Manager.Status(ctx, target.Unit)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("status query failed: %v", err))
		return servicemgr.ServiceStatus{}, exitRuntimeError
	}
	return status, exitSuccess
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
