package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

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
	dependencies, err := a.controlDependenciesFor(ctx, opts.backend)
	if err != nil {
		return observedProcesses(discoverer, selectors)
	}
	target, err := a.resolveControlTarget(ctx, opts, service, resolved.Tree, dependencies.backend, dependencies.manager, dependencies.resolver)
	if err != nil {
		return observedProcesses(discoverer, selectors)
	}
	if backendPIDs := app.ServiceBackendPIDs(ctx, target.Backend, target.Unit, target.BackendPIDs, a.Runner); backendPIDs != nil {
		discoverer.BackendPIDs = backendPIDs
	}
	discoverer.ProcessOwnership = func(id process.Identity) (bool, bool) {
		return servicemgr.CgroupOwnership(id.Cgroup, target.Unit)
	}
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
	if s == "" {
		return cliDisplayUnknown
	}
	return s
}
