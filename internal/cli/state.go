package cli

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/state"
)

// runState dispatches persistent state-store maintenance commands.
func (a App) runState(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError(commandState, "state supports only: compact [--before TIME]")
	}
	sub := opts.args[0]
	rest := opts.args[1:]
	if sub != commandStateCompact || len(rest) > 0 {
		return a.commandUsageError(commandState, "state supports only: compact [--before TIME]")
	}
	return a.runStateCompact(ctx, opts)
}

func (a App) runStateCompact(ctx context.Context, opts options) int {
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}

	before, err := parseBefore(opts.before, time.Now)
	if err != nil {
		return a.fail(opts, err.Error())
	}

	store, err := openStateStore(ctx, cfg)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("open state database: %v", err))
	}
	defer store.Close()

	// The store owns the sequence: consolidate and prune to the configured
	// retention as the daemon does on its rollup interval, drop whatever remains
	// older than an explicit --before, then vacuum. The deadline covers all of it.
	compactCtx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	result, err := store.CompactHistory(compactCtx, time.Now(), before)
	if err != nil {
		a.recordAccess(cfg, accessCommandStateCompact, "", accessStatusError, err.Error())
		return a.fail(opts, err.Error())
	}

	a.writeStateCompactResult(opts, result, before)
	a.recordAccess(cfg, accessCommandStateCompact, "", accessStatusOK, fmt.Sprintf("pruned %d rows", result.Pruned()))
	return exitSuccess
}

// writeStateCompactResult reports one compaction in JSON or as a single line.
func (a App) writeStateCompactResult(opts options, result state.MaintainResult, before time.Time) {
	cutoff := ""
	if !before.IsZero() {
		cutoff = before.UTC().Format(time.RFC3339)
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]any{
			cliJSONKeyPruned:   result.Pruned(),
			cliJSONKeyBefore:   cutoff,
			cliJSONKeyRolled:   result.Rolled,
			cliJSONKeyArchives: result.Archives,
			cliJSONKeyEvents:   result.Events,
			cliJSONKeyVacuum:   true,
		})
		return
	}
	scope := "to the configured retention"
	if cutoff != "" {
		scope = "to the configured retention and before " + cutoff
	}
	fmt.Fprintf(
		a.Stdout,
		"compacted state %s: consolidated %d row(s), pruned %d row(s) (archives=%d events=%d)\n",
		scope, result.Rolled, result.Pruned(), result.Archives, result.Events,
	)
}
