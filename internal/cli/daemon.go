package cli

import (
	"context"
	"fmt"
)

// runDaemon dispatches sermod control subcommands.
func (a App) runDaemon(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError(commandDaemon, "daemon requires subcommand reload")
	}
	if opts.args[0] == commandReload {
		if len(opts.args) > 1 {
			return a.commandUsageError(commandDaemon, "daemon reload takes no arguments")
		}
		return a.runReload(ctx, opts)
	}
	return a.commandUsageError(commandDaemon, fmt.Sprintf("unknown daemon subcommand %q", opts.args[0]))
}
