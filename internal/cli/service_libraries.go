package cli

import (
	"context"
	"errors"
	"fmt"

	"sermo/internal/appinspect"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

type serviceLibraries struct {
	State        servicemgr.Status           `json:"state,omitempty"`
	Name         string                      `json:"name"`
	CheckEnabled bool                        `json:"check_enabled"`
	Complete     bool                        `json:"complete"`
	Mapped       process.LibraryReport       `json:"mapped"`
	ELFSkipped   string                      `json:"elf_skipped,omitempty"`
	ELF          *checks.LibraryDependencies `json:"elf,omitempty"`
	Errors       []string                    `json:"errors,omitempty"`
	Skipped      string                      `json:"skipped,omitempty"`
}

// This command deliberately bypasses cached daemon results: deployment must
// exercise the candidate's scanner against the host before installing it.
func (a App) runServiceLibraries(ctx context.Context, opts options) int {
	if len(opts.notifyNames) > 0 {
		return a.commandUsageError(commandServices, "services libraries does not send notifications")
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	session, err := a.newOperationSession(ctx, opts, cfg, nil)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	names := opts.args
	if len(names) == 0 {
		names = cfg.ServiceNames
	}
	rows := make([]serviceLibraries, 0, len(names))
	for i, resolved := range cfg.ResolveServices(names) {
		if _, configured := cfg.CanonicalServiceName(names[i]); !configured {
			resolved.Resolved, resolved.Errors = cfg.ResolveCatalog(config.CategoryService, names[i])
		}
		rows = append(rows, inspectServiceLibraries(ctx, session, resolved))
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]any{commandServices: rows})
	} else {
		for _, row := range rows {
			fmt.Fprintf(a.Stdout, "%s: complete=%t processes=%d loaded=%d\n", row.Name, row.Complete, row.Mapped.Processes, len(row.Mapped.Dependencies))
			for _, err := range row.Errors {
				fmt.Fprintf(a.Stdout, "  %s\n", err)
			}
			if row.Skipped != "" {
				fmt.Fprintf(a.Stdout, "  %s\n", row.Skipped)
			}
		}
	}
	return exitSuccess
}

func inspectServiceLibraries(ctx context.Context, session *operationSession, resolution config.ServiceResolution) serviceLibraries {
	resolved := resolution.Resolved
	row := serviceLibraries{Name: resolved.Name, Errors: resolution.Errors}
	if len(row.Errors) > 0 {
		return row
	}
	configuredChecks, _ := resolved.Tree[config.SectionChecks].(map[string]any)
	for _, raw := range configuredChecks {
		entry, _ := raw.(map[string]any)
		if entry[checks.CheckKeyType] == checks.CheckTypeStaleLibraries && !cfgval.Disabled(entry) {
			row.CheckEnabled = true
		}
	}
	prepared, err := session.prepare(ctx, resolved.Name, resolved)
	if err != nil {
		row.Errors = append(row.Errors, err.Error())
		return row
	}
	if prepared.target.Backend != servicemgr.BackendSystemd && prepared.target.Backend != servicemgr.BackendOpenRC {
		row.Skipped = "library inspection requires a native service"
		return row
	}
	if prepared.runtime.NoResidentProcess {
		row.Skipped = "service has no resident process"
		return row
	}
	row.Mapped, err = prepared.runtime.CheckDeps.StaleLibraries(ctx)
	if err != nil {
		row.Errors = append(row.Errors, err.Error())
	}
	if binary := appinspect.BinaryPath(resolved.Tree); binary != "" {
		elf, elfErr := checks.InspectLibraries(ctx, binary)
		row.ELF = &elf
		if errors.Is(elfErr, checks.ErrLibraryScript) {
			row.ELF = nil
			row.ELFSkipped = elfErr.Error()
		} else if elfErr != nil {
			row.Errors = append(row.Errors, elfErr.Error())
		}
	}
	if row.Mapped.Processes == 0 && len(row.Errors) == 0 {
		status, statusErr := prepared.target.Manager.Status(ctx, prepared.target.Unit)
		row.State = status.Status
		switch {
		case statusErr != nil:
			row.Errors = append(row.Errors, statusErr.Error())
		case status.Status == servicemgr.StatusInactive || status.Status == servicemgr.StatusFailed:
			row.Skipped = "no running service processes available for library inspection"
		default:
			row.Errors = append(row.Errors, fmt.Sprintf("service state %s has no verified processes for library inspection", status.Status))
		}
	}
	row.Complete = len(row.Errors) == 0 && row.Mapped.Processes > 0
	return row
}
