package cli

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"

	"sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/operation"
	"sermo/internal/process"
)

// runProcesses discovers and reports the processes belonging to a service,
// reading the service's `processes` selectors from resolved config.
func (a App) runProcesses(ctx context.Context, opts options) int {
	cfg, service, resolved, code := a.resolveServiceCommand(opts, commandProcesses)
	if code != exitSuccess {
		return code
	}

	selectors, warnings := process.ParseSelectors(resolved.Tree)
	procs, discWarnings := a.discoverProcesses(ctx, opts, cfg, resolved, service, selectors)
	warnings = append(warnings, discWarnings...)

	return renderServiceList(a, opts, service, cliJSONKeyProcesses, procs,
		warnings, "no processes found for %s\n", formatProcess)
}

func (a App) discoverProcesses(ctx context.Context, opts options, cfg *config.Config, resolved config.Resolved, service string, selectors []process.Selector) ([]process.Process, []string) {
	if a.Discover != nil {
		return a.Discover(selectors)
	}
	discoverer := process.NewDiscovererWithUserLookup(app.EngineUserLookup(cfg, a.Runner))
	discoverer.KeepChildren = process.KeepChildren(resolved.Tree)
	dependencies, err := a.controlDependenciesFor(ctx, opts.backend)
	if err != nil {
		return observedProcesses(discoverer, selectors)
	}
	target, err := a.resolveControlTarget(ctx, opts, service, resolved.Tree, dependencies.backend, dependencies.manager, dependencies.resolver)
	if err != nil {
		return observedProcesses(discoverer, selectors)
	}
	if query := app.ServiceBackendPIDs(ctx, target.Backend, target.Unit, target.BackendPIDs, a.Runner); query != nil {
		discoverer.BackendPIDs = process.DiscoveryPIDs(query)
	}
	discoverer.ProcessOwnership = operation.UnitProcessOwnership(target.Unit)
	return observedProcesses(discoverer, selectors)
}

func observedProcesses(discoverer process.Discoverer, selectors []process.Selector) ([]process.Process, []string) {
	observation, err := discoverer.Observe(selectors)
	if err != nil {
		observation.Warnings = append(observation.Warnings, err.Error())
	}
	return observation.Processes, observation.Warnings
}

func formatProcess(p process.Process) string {
	key, value := processDisplayField(p)
	line := fmt.Sprintf("pid=%d ppid=%d user=%s %s=%s source=%s", p.PID, p.PPID, orUnknown(p.User), key, value, p.Source)
	if p.Role != "" {
		line += " role=" + p.Role
	}
	// A stray carries the backend seed's role "main", which on its own reads as the
	// service's principal process; the flag is what tells the operator that nothing
	// in the configuration accounts for this process.
	if p.Stray {
		line += " stray=true"
	}
	if p.External {
		line += " external=true"
	}
	if p.SignalBlockReason != "" {
		line += " blocked=" + strconv.Quote(p.SignalBlockReason)
	}
	return line
}

func processDisplayField(p process.Process) (key, value string) {
	if p.ExeOK && p.Exe != "" {
		return process.SelectorKeyExe, p.Exe
	}
	if p.ExePrev != "" {
		return "exe_previous", strconv.Quote(p.ExePrev + " (deleted)")
	}
	if cmd := strings.TrimSpace(strings.Join(p.Cmdline, " ")); cmd != "" {
		return process.SelectorKeyCmd, strconv.Quote(cmd)
	}
	if p.Exe != "" {
		return process.SelectorKeyExe, p.Exe
	}
	return process.SelectorKeyExe, cliDisplayUnknown
}

func orUnknown(s string) string {
	return cmp.Or(s, cliDisplayUnknown)
}
