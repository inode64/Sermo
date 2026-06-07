package cli

import (
	"fmt"
	"path/filepath"

	"sermo/internal/diag"
	"sermo/internal/state"
)

// runDiagnose runs configuration- and host-consistency diagnostics: it validates
// the config, checks the state database, flags stored data for services that no
// longer exist, warns about per-check intervals not aligned with the global
// resolution, and reports referenced interfaces, files/directories and mount
// points that do not exist. Exit code is non-zero when any error-level finding is
// reported (warnings alone exit 0).
func (a App) runDiagnose(opts options) int {
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

func (a App) writeDiagnoseText(r diag.Result) {
	for _, f := range r.Findings {
		fmt.Fprintf(a.Stdout, "%-7s %s: %s\n", f.Level, f.Scope, f.Message)
	}
	fmt.Fprintf(a.Stdout, "%d error(s), %d warning(s)\n", r.Errors(), r.Warnings())
	if len(r.Findings) == 0 || (r.Errors() == 0 && r.Warnings() == 0) {
		fmt.Fprintln(a.Stdout, "ok: no problems found")
	}
}

func (a App) writeDiagnoseJSON(r diag.Result) {
	findings := make([]map[string]any, 0, len(r.Findings))
	for _, f := range r.Findings {
		findings = append(findings, map[string]any{"level": f.Level, "scope": f.Scope, "message": f.Message})
	}
	writeJSON(a.Stdout, map[string]any{
		"findings": findings,
		"errors":   r.Errors(),
		"warnings": r.Warnings(),
	})
}
