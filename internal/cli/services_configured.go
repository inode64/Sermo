package cli

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/state"
)

// configuredServiceStateError marks a service whose definition or control
// target could not be resolved, so no backend state exists for it.
const configuredServiceStateError = "error"

// configuredService is one row of `sermoctl services`: a service this host is
// configured to supervise, whatever its control backend (init unit, Docker
// container or libvirt domain/network).
type configuredService struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Backend     string `json:"backend"`
	Unit        string `json:"unit,omitempty"`
	State       string `json:"state"`
	Monitored   bool   `json:"monitored"`
	Enabled     bool   `json:"enabled"`
	Error       string `json:"error,omitempty"`
}

// reportTone classifies the service for the --notify report: a service Sermo
// cannot even resolve is an issue whatever its monitoring, unmonitored and
// disabled services are muted, states that need attention are issues, the
// rest is ok.
func (s configuredService) reportTone() reportTone {
	switch {
	case s.State == configuredServiceStateError:
		return reportToneIssue
	case !s.Enabled || !s.Monitored:
		return reportToneMuted
	case app.ServiceStateNeedsAttention(s.State):
		return reportToneIssue
	default:
		return reportToneOK
	}
}

// runConfiguredServices lists the configured services. It prefers the running
// daemon's computed view (one GET of the services API) and probes locally,
// like status does, any service the daemon did not report.
func (a App) runConfiguredServices(ctx context.Context, opts options) int {
	if len(opts.args) > 0 {
		return a.commandUsageError(commandServices, commandServices+" accepts no arguments; use `"+commandServices+" "+commandArgCatalog+" [all]` for the catalog")
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	// The listing and the --notify send each get the full --timeout: a slow
	// backend probe must not leave the notifiers an expiring deadline.
	listCtx, cancel := context.WithTimeout(ctx, opts.timeout)
	services, code := a.configuredServices(listCtx, opts, cfg)
	cancel()
	if code != exitSuccess {
		return code
	}

	var notified []string
	if len(opts.notifyNames) > 0 {
		notified, code = a.sendServicesReport(ctx, opts, cfg, configuredServicesReportMessage(services, time.Now()))
		if code != exitSuccess {
			return code
		}
	}
	if opts.json {
		out := map[string]any{commandServices: services}
		if notified != nil {
			out[cliJSONKeyNotified] = notified
		}
		writeJSON(a.Stdout, out)
		return exitSuccess
	}
	a.printConfiguredServices(services)
	if notified != nil && !opts.quiet {
		fmt.Fprintf(a.Stdout, "sent services report to %s\n", strings.Join(notified, ", "))
	}
	return exitSuccess
}

// configuredServices builds one row per configured service, in config order.
// Only the services the daemon did not report are resolved and probed.
func (a App) configuredServices(ctx context.Context, opts options, cfg *config.Config) ([]configuredService, int) {
	daemon := a.fetchDaemonServices(ctx, cfg)
	services := make([]configuredService, len(cfg.ServiceNames))
	var local []int
	var localNames []string
	for i, name := range cfg.ServiceNames {
		if canonical, ok := cfg.CanonicalServiceName(name); ok {
			name = canonical
		}
		if d, ok := daemon[name]; ok {
			services[i] = configuredService{
				Name: name, DisplayName: d.DisplayName, Backend: d.Backend, Unit: d.Unit,
				State: d.State, Monitored: d.Monitored, Enabled: d.Enabled,
			}
			continue
		}
		local = append(local, i)
		localNames = append(localNames, name)
	}
	if len(local) == 0 {
		return services, exitSuccess
	}

	dependencies, err := a.controlDependenciesFor(ctx, opts.backend)
	if err != nil {
		a.reportError(opts, err.Error())
		return nil, exitRuntimeError
	}
	stored := storedMonitorStates(ctx, cfg)
	resolutions := cfg.ResolveServices(localNames)
	// Each probe is a few init-backend, libvirt or Docker queries: bound them
	// like the daemon's own startup does, and keep each probe's warnings in a
	// buffer so they print in config order rather than interleaved.
	warnings := make([]bytes.Buffer, len(local))
	limit := config.EngineInt(cfg, config.EngineKeyMaxParallelChecks, app.DefaultEngineMaxParallelChecks)
	app.ForEachParallel(len(local), limit, func(j int) {
		probe := a
		probe.Stderr = &warnings[j]
		services[local[j]] = probe.probeConfiguredService(ctx, opts, dependencies, stored, resolutions[j])
	})
	for j := range warnings {
		_, _ = warnings[j].WriteTo(a.Stderr)
	}
	return services, exitSuccess
}

// probeConfiguredService derives one service's row from its control backend
// only, as status does when sermod is not answering.
func (a App) probeConfiguredService(ctx context.Context, opts options, dependencies controlDependencies, stored map[string]state.MonitorRecord, res config.ServiceResolution) configuredService {
	name := res.Resolved.Name
	// Until its configuration says otherwise a service is enabled and
	// monitored, as the daemon assumes when nothing is recorded.
	row := configuredService{Name: name, DisplayName: name, Enabled: true, Monitored: true}
	fail := func(msg string) configuredService {
		row.State = configuredServiceStateError
		row.Error = msg
		if !opts.quiet {
			fmt.Fprintf(a.Stderr, cliWarningFormat, "service "+name+": "+msg)
		}
		return row
	}
	if len(res.Errors) > 0 {
		return fail("config resolve failed: " + res.Errors[0])
	}
	tree := res.Resolved.Tree
	row.DisplayName = config.DisplayName(tree, name)
	mon := configuredMonitorView(tree)
	if record, ok := stored[name]; ok {
		mon = mon.withRecord(record)
	}
	row.Enabled, row.Monitored = mon.Enabled, mon.Monitored()

	target, err := a.resolveControlTarget(ctx, opts, name, tree, dependencies.backend, dependencies.manager, dependencies.resolver)
	if err != nil {
		return fail(fmt.Sprintf("control target failed: %v", err))
	}
	row.Backend, row.Unit = string(target.Backend), target.Unit
	if !row.Enabled {
		row.State = app.TargetStateDisabled
		return row
	}
	status, err := target.Manager.Status(ctx, target.Unit)
	if err != nil {
		return fail(fmt.Sprintf("status query failed: %v", err))
	}
	row.State = localServiceState(status, mon)
	return row
}

func (a App) printConfiguredServices(services []configuredService) {
	if len(services) == 0 {
		fmt.Fprintln(a.Stdout, "no configured services")
		return
	}
	tw := newTabWriter(a.Stdout)
	fmt.Fprintln(tw, "SERVICE\tTYPE\tSTATE\tMONITORED")
	for _, s := range services {
		monitored := "no"
		if s.Monitored {
			monitored = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, cmp.Or(s.Backend, "-"), s.State, monitored)
	}
	_ = tw.Flush()
}
