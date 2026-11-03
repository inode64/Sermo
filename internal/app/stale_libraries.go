package app

import (
	"context"
	"fmt"
	"strings"

	"sermo/internal/checks"
	"sermo/internal/process"
)

func serviceStaleLibraries(discoverer process.Discoverer, selectors []process.Selector) checks.StaleLibrariesFunc {
	return func(ctx context.Context) (process.LibraryReport, error) {
		if err := ctx.Err(); err != nil {
			return process.LibraryReport{}, fmt.Errorf("observe library processes: %w", err)
		}
		observation, err := discoverer.ObserveCached(selectors)
		if err != nil {
			return process.LibraryReport{}, fmt.Errorf("observe library processes: %w", err)
		}
		if warnings := process.UncertainWarnings(observation.Warnings); len(warnings) > 0 {
			return process.LibraryReport{}, fmt.Errorf("incomplete library process discovery: %s", strings.Join(warnings, "; "))
		}
		procs := observation.Processes
		if procs == nil {
			procs = []process.Process{}
		}
		report, err := process.ScanLibraries(ctx, procs)
		principal := libraryRestartPrincipal(discoverer, selectors, procs)
		for i := range report.Libraries {
			report.Libraries[i].RestartEligible = libraryRestartEligible(report.Libraries[i], principal)
		}
		if err != nil {
			return report, fmt.Errorf("scan service libraries: %w", err)
		}
		return report, nil
	}
}

// libraryRestartEligible needs the unique main process and the same filesystem
// root as Sermo: a chrooted finding is read through Sermo's view of the path, not
// the process's, which is enough to alert but not to act on.
func libraryRestartEligible(lib process.StaleLibrary, principal int) bool {
	return principal != 0 && lib.PID == principal && !lib.ChrootedProcess
}

// A unique strict main identity can justify restarting its service. Backend
// attribution alone, inherited children, delegated workloads and ambiguous
// multiple masters remain diagnostic. This also prevents an old SSH session
// from repeatedly restarting a newly upgraded listener.
//
// A main pidfile settles which of several processes of that identity is the
// daemon: a mail server detaches short-lived deliveries that run its executable
// as its user, and they would read as further masters for as long as they live.
func libraryRestartPrincipal(discoverer process.Discoverer, selectors []process.Selector, procs []process.Process) int {
	var main []process.Selector
	for _, selector := range selectors {
		if selector.Name == process.RoleMain && selector.IsStrictPrincipal() {
			main = append(main, selector)
		}
	}
	// Catalog profiles may name their only principal after the daemon (for
	// example postgres). An explicit main selector still takes precedence over
	// workers; otherwise the main pidfile or a unique backend main must settle
	// which strictly identified process owns the service.
	if len(main) == 0 {
		for _, selector := range selectors {
			if selector.IsStrictPrincipal() {
				main = append(main, selector)
			}
		}
	}
	principal, matches := 0, 0
	for _, proc := range procs {
		if (proc.Role != process.RoleMain && !proc.MainPidfile) || proc.Source == process.SourceChild || proc.Delegated || proc.Stray || proc.External || !proc.ExeOK {
			continue
		}
		if _, ok := discoverer.StrictMatchPID(proc.PID, main); !ok {
			continue
		}
		if proc.MainPidfile {
			return proc.PID
		}
		principal = proc.PID
		matches++
	}
	if matches != 1 {
		return 0
	}
	return principal
}
