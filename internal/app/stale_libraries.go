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
func libraryRestartPrincipal(discoverer process.Discoverer, selectors []process.Selector, procs []process.Process) int {
	var main []process.Selector
	for _, selector := range selectors {
		if selector.Name == process.RoleMain && selector.IsStrictPrincipal() {
			main = append(main, selector)
		}
	}
	principal := 0
	for _, proc := range procs {
		if proc.Role != process.RoleMain || proc.Source == process.SourceChild || proc.Delegated || proc.Stray || proc.External || !proc.ExeOK {
			continue
		}
		if _, ok := discoverer.StrictMatchPID(proc.PID, main); !ok {
			continue
		}
		if principal != 0 {
			return 0
		}
		principal = proc.PID
	}
	return principal
}
