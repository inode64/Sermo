package cli

import (
	"context"
	"fmt"

	"sermo/internal/app"
)

// runWatch dispatches host-watch queries against the running daemon.
func (a App) runWatch(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError("watch", "watch requires subcommand status")
	}
	switch opts.args[0] {
	case "status":
		return a.runWatchStatus(ctx, opts)
	default:
		return a.commandUsageError("watch", fmt.Sprintf("unknown watch subcommand %q", opts.args[0]))
	}
}

func (a App) runWatchStatus(ctx context.Context, opts options) int {
	if len(opts.args) != 2 {
		return a.commandUsageError("watch", "watch status requires exactly one watch name")
	}
	name := opts.args[1]
	state := app.TargetStateOK
	if a.FetchDaemonWatchState != nil {
		if st, ok := a.FetchDaemonWatchState(ctx, opts, name); ok && st != "" {
			state = st
		}
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]string{"watch": name, "state": state})
		return exitSuccess
	}
	fmt.Fprintf(a.Stdout, "%s state=%s\n", name, state)
	return exitSuccess
}
