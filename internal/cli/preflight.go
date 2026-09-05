package cli

import (
	"context"
	"fmt"

	"sermo/internal/app"
	"sermo/internal/checks"
)

// runPreflight resolves a service and runs the prepared operation engine's
// canonical preflight pipeline. A required check failure exits 1.
func (a App) runPreflight(ctx context.Context, opts options) int {
	cfg, service, resolved, code := a.resolveServiceCommand(opts, commandPreflight)
	if code != exitSuccess {
		return code
	}

	session, err := a.newOperationSession(ctx, opts, cfg, nil)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	prepared, err := session.prepare(ctx, service, resolved)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("control target failed: %v", err))
	}

	ctx, cancel := context.WithTimeout(ctx, app.PreflightDeadline(prepared.runtime.CheckDeps.DefaultTimeout))
	defer cancel()
	outcome := prepared.runtime.Engine.Preflight(ctx)

	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyService: service, cliJSONKeyOK: outcome.OK, cliJSONKeyChecks: outcome.Results})
	} else {
		a.printPreflight(service, outcome)
	}

	if outcome.OK {
		return exitSuccess
	}
	return exitNotActive
}

func (a App) printPreflight(service string, outcome checks.Outcome) {
	overall := cliTextOK
	if !outcome.OK {
		overall = cliTextFail
	}
	if len(outcome.Results) == 0 {
		fmt.Fprintf(a.Stdout, "preflight %s: %s (no checks)\n", service, overall)
		return
	}
	fmt.Fprintf(a.Stdout, "preflight %s: %s\n", service, overall)
	for _, r := range outcome.Results {
		tag := cliTextOK
		if !r.OK {
			tag = cliTextFail
			if r.Optional {
				tag = cliTextWarn
			}
		}
		fmt.Fprintf(a.Stdout, "  %-4s %s: %s\n", tag, r.Check, r.Message)
	}
}
