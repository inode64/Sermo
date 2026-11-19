package cli

import (
	"context"
	"fmt"
	"time"

	sermoapp "sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/state"
)

// Notification mute results, as --json reports them.
const (
	muteStatusMuted    = "muted"
	muteStatusUnmuted  = "unmuted"
	muteStatusNotMuted = "not-muted"
)

// runMute mutes (`mute`) or unmutes (`unmute`) a service's notifications. The
// mute lives in the persistent store under engine.state, so it survives daemon
// restarts, and the daemon reads it on every notification. Checks, rules and
// remediation keep running; the service's watches follow its mute.
func (a App) runMute(ctx context.Context, opts options, mute bool) int {
	verb := muteVerb(mute)
	service := opts.service()
	if code := a.requireSingleServiceName(service != "", len(opts.args), verb, verb); code != exitSuccess {
		return code
	}
	d, code := a.muteDuration(opts, verb, mute)
	if code != exitSuccess {
		return code
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	service, code = a.canonicalService(opts, cfg, service)
	if code != exitSuccess {
		return code
	}
	return a.applyNotifyMute(ctx, opts, cfg, service, service, verb, d, map[string]any{cliJSONKeyService: service})
}

// runWatchMute mutes or unmutes one watch's notifications by its name: a host
// watch or a service watch ("<service>:<watch>").
func (a App) runWatchMute(ctx context.Context, opts options, mute bool) int {
	verb := muteVerb(mute)
	if len(opts.args) != watchCommandTargetArgCount {
		return a.commandUsageError(commandWatch, fmt.Sprintf("watch %s requires exactly one watch name", verb))
	}
	name := opts.args[1]
	d, code := a.muteDuration(opts, commandWatch, mute)
	if code != exitSuccess {
		return code
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	if !knownWatchName(cfg, name) {
		return a.fail(opts, unknownWatchMessage(cfg, name))
	}
	return a.applyNotifyMute(ctx, opts, cfg, sermoapp.WatchMonitorKey(name), "watch "+name, "watch "+verb, d, map[string]any{cliJSONKeyWatch: name})
}

func muteVerb(mute bool) string {
	if mute {
		return commandMute
	}
	return commandUnmute
}

// muteDuration is the mute's --for (DefaultNotificationMute when omitted), or
// zero for an unmute, which takes no duration.
func (a App) muteDuration(opts options, usage string, mute bool) (time.Duration, int) {
	if !mute {
		if opts.muteForSet {
			return 0, a.commandUsageError(usage, "--for applies to mute only")
		}
		return 0, exitSuccess
	}
	if !opts.muteForSet {
		return sermoapp.DefaultNotificationMute, exitSuccess
	}
	if err := sermoapp.ValidateNotificationMute(opts.muteFor); err != nil {
		return 0, a.commandUsageError(usage, err.Error())
	}
	return opts.muteFor, exitSuccess
}

func (a App) applyNotifyMute(ctx context.Context, opts options, cfg *config.Config, key, subject, verb string, d time.Duration, payload map[string]any) int {
	return withStateStore(ctx, cfg, func(err error) int {
		return a.fail(opts, fmt.Sprintf("%s failed: %v", verb, err))
	}, func(store *state.Store) int {
		until, changed, err := sermoapp.ApplyNotificationMute(store, key, d, state.SourceCLI, time.Now())
		if err != nil {
			a.recordAccess(cfg, verb, key, accessStatusError, err.Error())
			return a.fail(opts, fmt.Sprintf("%s failed: %v", verb, err))
		}
		message := sermoapp.NotificationMuteMessage(until, changed)
		a.recordAccess(cfg, verb, key, accessStatusOK, message)
		if opts.json {
			payload[cliJSONKeyNotifications] = muteStatus(until, changed)
			if !until.IsZero() {
				payload[cliJSONKeyMutedUntil] = until.UTC().Format(time.RFC3339)
			}
			writeJSON(a.Stdout, payload)
			return exitSuccess
		}
		fmt.Fprintf(a.Stdout, "%s: %s\n", subject, message)
		return exitSuccess
	})
}

func muteStatus(until time.Time, changed bool) string {
	switch {
	case !until.IsZero():
		return muteStatusMuted
	case changed:
		return muteStatusUnmuted
	default:
		return muteStatusNotMuted
	}
}
