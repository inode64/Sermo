package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"sermo/internal/diag"
	"sermo/internal/state"
)

// runDiagnose runs configuration- and host-consistency diagnostics: it validates
// the config, checks the state database, flags stored control state for
// services/watches that no longer exist, warns about per-check intervals not
// aligned with the global resolution, and reports referenced interfaces,
// files/directories and mount
// points that do not exist. Exit code is non-zero when any error-level finding is
// reported (warnings alone exit 0).
func (a App) runDiagnose(opts options) int {
	if len(opts.args) > 0 {
		if opts.args[0] == "clean" && len(opts.args) == 1 {
			return a.runDiagnoseClean(opts)
		}
		return a.commandUsageError("diagnose", "diagnose supports only: diagnose [--json] | diagnose clean [--json]")
	}

	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}

	// The store is best-effort: if it cannot be opened, diagnostics still run with
	// nil (database checks become informational).
	var store diag.Store
	if s, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename)); err == nil {
		defer s.Close()
		store = s
	}

	result := diag.Diagnose(cfg, store, diag.OSHost{})

	if opts.json {
		a.writeDiagnoseJSON(result)
	} else {
		a.writeDiagnoseText(result)
	}
	if result.Errors() > 0 {
		return exitConfigInvalid
	}
	return exitSuccess
}

func (a App) runDiagnoseClean(opts options) int {
	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}
	store, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		return a.fail(opts, fmt.Sprintf("open state database: %v", err))
	}
	defer store.Close()

	result, err := store.PruneUnconfiguredControlStates(diag.ConfiguredStoredNames(cfg))
	if err != nil {
		return a.fail(opts, fmt.Sprintf("clean diagnostic state: %v", err))
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]any{
			"pruned":   result.Rows,
			"services": result.Services,
		})
		return exitSuccess
	}
	if len(result.Services) == 0 {
		fmt.Fprintln(a.Stdout, "no unconfigured control state found")
		return exitSuccess
	}
	fmt.Fprintf(
		a.Stdout,
		"cleaned control state for %d unconfigured target(s): %s (%d row(s))\n",
		len(result.Services),
		strings.Join(result.Services, ", "),
		result.Rows,
	)
	return exitSuccess
}

func (a App) writeDiagnoseText(r diag.Result) {
	tw := tabwriter.NewWriter(a.Stdout, 0, 0, 2, ' ', 0)
	for _, f := range r.Findings {
		fmt.Fprintf(tw, "%s\t%s: %s\n", f.Level, f.Scope, f.Message)
	}
	_ = tw.Flush()
	errors, warnings := r.Errors(), r.Warnings()
	fmt.Fprintf(a.Stdout, "%d error(s), %d warning(s)\n", errors, warnings)
	if len(r.Findings) == 0 || (errors == 0 && warnings == 0) {
		fmt.Fprintln(a.Stdout, "ok: no problems found")
	}
}

func (a App) writeDiagnoseJSON(r diag.Result) {
	findings := make([]map[string]any, 0, len(r.Findings))
	for _, f := range r.Findings {
		findings = append(findings, map[string]any{"level": f.Level, "scope": f.Scope, "message": f.Message})
	}
	errors, warnings := r.Errors(), r.Warnings()
	writeJSON(a.Stdout, map[string]any{
		"findings": findings,
		"errors":   errors,
		"warnings": warnings,
	})
}
