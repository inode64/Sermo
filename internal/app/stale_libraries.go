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
			report.Libraries[i].RestartEligible = principal != 0 && report.Libraries[i].PID == principal
		}
		if err != nil {
			return report, fmt.Errorf("scan service libraries: %w", err)
		}
		return report, nil
	}
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
