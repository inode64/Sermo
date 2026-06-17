package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"sermo/internal/state"
)

const stateHistoryRetention = 366 * 24 * time.Hour

// runState dispatches persistent state-store maintenance commands.
func (a App) runState(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError("state", "state supports only: compact [--before TIME]")
	}
	sub := opts.args[0]
	rest := opts.args[1:]
	if sub != "compact" || len(rest) > 0 {
		return a.commandUsageError("state", "state supports only: compact [--before TIME]")
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
	if before.IsZero() {
		before = time.Now().Add(-stateHistoryRetention)
	}

	store, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		return a.fail(opts, fmt.Sprintf("open state database: %v", err))
	}
	defer store.Close()

	result, err := store.PruneHistory(before)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("prune state history: %v", err))
	}

	compactCtx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	if err := store.Compact(compactCtx); err != nil {
		return a.fail(opts, fmt.Sprintf("compact state database: %v", err))
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{
			"pruned":          result.Rows,
			"before":          before.UTC().Format(time.RFC3339),
			"sla":             result.SLA,
			"measurements":    result.Measurements,
			"metrics":         result.Metrics,
			"daemon_metrics":  result.DaemonMetrics,
			"service_metrics": result.ServiceMetrics,
			"events":          result.Events,
			"vacuum":          true,
		})
		return exitSuccess
	}

	fmt.Fprintf(
		a.Stdout,
		"compacted state before %s: pruned %d row(s) (sla=%d measurements=%d metrics=%d daemon_metrics=%d service_metrics=%d events=%d)\n",
		before.UTC().Format(time.RFC3339),
		result.Rows,
		result.SLA,
		result.Measurements,
		result.Metrics,
		result.DaemonMetrics,
		result.ServiceMetrics,
		result.Events,
	)
	return exitSuccess
}
