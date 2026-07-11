package cli

import (
	"context"
	"fmt"

	"sermo/internal/notify"
)

// runNotifier executes explicit operator actions for one configured notifier.
func (a App) runNotifier(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError(commandNotifier, "notifier requires a subcommand")
	}
	if opts.args[0] != commandNotifierTest {
		return a.commandUsageError(commandNotifier, fmt.Sprintf("unknown notifier subcommand %q", opts.args[0]))
	}
	if len(opts.args) != 2 {
		return a.commandUsageError(commandNotifier, "notifier test requires one notifier name")
	}

	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}
	registry, warnings := a.BuildNotifiers(cfg)
	for _, warning := range warnings {
		fmt.Fprintf(a.Stderr, cliWarningFormat, warning)
	}
	name := opts.args[1]
	n, ok := registry[name]
	if !ok {
		return a.commandUsageError(commandNotifier, fmt.Sprintf("unknown or disabled notifier %q", name))
	}

	sendCtx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	if err := n.Send(sendCtx, notify.TestMessage()); err != nil {
		return a.fail(opts, fmt.Sprintf("send test notification to %s: %v", name, err))
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyOK: true, cliJSONKeyTarget: name})
		return exitSuccess
	}
	if !opts.quiet {
		fmt.Fprintf(a.Stdout, "test notification sent to %s\n", name)
	}
	return exitSuccess
}
