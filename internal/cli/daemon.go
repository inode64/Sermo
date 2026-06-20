package cli

import (
	"context"
	"fmt"
)

// runDaemon dispatches sermod control subcommands.
func (a App) runDaemon(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError("daemon", "daemon requires subcommand reload")
	}
	if opts.args[0] == "reload" {
		if len(opts.args) > 1 {
			return a.commandUsageError("daemon", "daemon reload takes no arguments")
		}
		return a.runReload(ctx, opts)
	}
	return a.commandUsageError("daemon", fmt.Sprintf("unknown daemon subcommand %q", opts.args[0]))
}
