package cli

import (
	"context"
	"fmt"
	"time"

	sermoapp "sermo/internal/app"
	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/state"
	"sermo/internal/web"
)

// notifierTargetArgCount is a notifier subcommand plus the notifier it names.
const notifierTargetArgCount = 2

// runNotifier executes explicit operator actions on the configured notifiers.
func (a App) runNotifier(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError(commandNotifier, "notifier requires subcommand list, test, mute or unmute")
	}
	switch opts.args[0] {
	case commandNotifierList:
		return a.runNotifierList(ctx, opts)
	case commandNotifierTest:
		return a.runNotifierTest(ctx, opts)
	case commandMute:
		return a.runNotifierMute(ctx, opts, true)
	case commandUnmute:
		return a.runNotifierMute(ctx, opts, false)
	}
	return a.commandUsageError(commandNotifier, fmt.Sprintf("unknown notifier subcommand %q", opts.args[0]))
}

// runNotifierMute mutes or unmutes one notifier for the whole host: nothing
// the daemon monitors reaches it meanwhile, and the other notifiers keep
// hearing. An explicit `notifier test` still goes through.
func (a App) runNotifierMute(ctx context.Context, opts options, mute bool) int {
	verb := muteVerb(mute)
	if len(opts.args) != notifierTargetArgCount {
		return a.commandUsageError(commandNotifier, fmt.Sprintf("notifier %s requires one notifier name", verb))
	}
	name := opts.args[1]
	d, code := a.muteDuration(opts, commandNotifier, mute)
	if code != exitSuccess {
		return code
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	if _, known := cfg.Notifiers()[name]; !known {
		return a.fail(opts, fmt.Sprintf("unknown notifier %q", name))
	}
	return a.applyNotifyMute(ctx, opts, cfg, sermoapp.NotifierMuteKey(name), "notifier "+name, "notifier "+verb, d, map[string]any{cliJSONKeyNotifier: name})
}

// runNotifierList prints every configured notifier with the mute that holds
// over it, read from the persistent store like the daemon reads it.
func (a App) runNotifierList(ctx context.Context, opts options) int {
	if len(opts.args) != 1 {
		return a.commandUsageError(commandNotifier, "notifier list takes no arguments")
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	mutes, err := storedNotificationMutes(ctx, cfg)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("notifier list failed: %v", err))
	}
	list := configuredNotifiers(cfg, mutes, time.Now())
	if opts.json {
		writeJSON(a.Stdout, list)
		return exitSuccess
	}
	tw := newTabWriter(a.Stdout)
	fmt.Fprintln(tw, "NAME\tTYPE\tSTATE")
	for _, n := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", n.Name, n.Type, notifierStateText(n))
	}
	_ = tw.Flush()
	return exitSuccess
}

// configuredNotifiers is the notifier list as the dashboard reports it, with
// the mutes that hold at now.
func configuredNotifiers(cfg *config.Config, mutes map[string]state.NotificationMuteRecord, now time.Time) []web.Notifier {
	entries := cfg.Notifiers()
	list := make([]web.Notifier, 0, len(entries))
	for _, name := range notifierNames(cfg) {
		entry, _ := entries[name].(map[string]any)
		typ := cfgval.AsString(entry[notify.KeyType])
		n := web.Notifier{
			Name:        name,
			Type:        typ,
			Enabled:     !cfgval.Disabled(entry),
			Summary:     notify.ConfigSummary(typ, entry),
			MinSeverity: notify.EntryMinSeverity(entry).String(),
		}
		if mute, found := mutes[sermoapp.NotifierMuteKey(name)]; found && mute.MutedAt(now) {
			n.NotifyMutedUntil, n.NotifyMuteSource = mute.Until.UTC().Format(time.RFC3339), mute.Source
		}
		list = append(list, n)
	}
	return list
}

func notifierStateText(n web.Notifier) string {
	until, err := time.Parse(time.RFC3339, n.NotifyMutedUntil)
	switch {
	case !n.Enabled:
		return "disabled"
	case err == nil:
		return "muted until " + until.Local().Format(time.DateTime) + metaSuffix(n.NotifyMuteSource, "")
	default:
		return "enabled"
	}
}

// runNotifierTest sends an explicit test message through one enabled notifier.
func (a App) runNotifierTest(ctx context.Context, opts options) int {
	if len(opts.args) != notifierTargetArgCount {
		return a.commandUsageError(commandNotifier, "notifier test requires one notifier name")
	}

	cfg, code := a.loadConfig(opts)
	if cfg == nil {
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

	// Match the Web UI test: an SMTP or webhook round trip (TLS, auth) routinely
	// exceeds the 2s probe budget, so use engine.default_timeout unless the
	// operator chose --timeout.
	timeout := opts.timeout
	if !opts.timeoutSet {
		timeout = engineDefaultTimeout(cfg)
	}
	sendCtx, cancel := context.WithTimeout(ctx, timeout)
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
