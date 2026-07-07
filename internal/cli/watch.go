package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/state"
)

// runWatch dispatches host-watch queries against the running daemon.
func (a App) runWatch(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError(commandWatch, "watch requires subcommand status, monitor or unmonitor")
	}
	switch opts.args[0] {
	case commandStatus:
		return a.runWatchStatus(ctx, opts)
	case commandMonitor:
		return a.runWatchMonitor(opts, false)
	case commandUnmonitor:
		return a.runWatchMonitor(opts, true)
	default:
		return a.commandUsageError(commandWatch, fmt.Sprintf("unknown watch subcommand %q", opts.args[0]))
	}
}

// runWatchMonitor pauses (`unmonitor`) or resumes (`monitor`) a single watch by
// its name — a host watch or a service-embedded watch ("<service>:<watch>"). The
// state persists under paths.state keyed independently of any service, so
// unmonitoring a service never touches its watches and vice versa. The daemon
// reads this key live each cycle.
func (a App) runWatchMonitor(opts options, pause bool) int {
	verb := commandMonitor
	if pause {
		verb = commandUnmonitor
	}
	if len(opts.args) != 2 {
		return a.commandUsageError(commandWatch, fmt.Sprintf("watch %s requires exactly one watch name", verb))
	}
	name := opts.args[1]

	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}
	if !knownWatchName(cfg, name) {
		return a.fail(opts, fmt.Sprintf("unknown watch %q", name))
	}
	store, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		return a.fail(opts, fmt.Sprintf("watch %s failed: %v", verb, err))
	}
	defer store.Close()

	key := app.WatchMonitorKey(name)
	active, found, err := store.Active(key)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("watch %s failed: %v", verb, err))
	}
	if err := store.SetActive(key, !pause, state.SourceCLI); err != nil {
		return a.fail(opts, fmt.Sprintf("watch %s failed: %v", verb, err))
	}
	status := monitorStatusResumed
	switch {
	case pause:
		status = monitorStatusPaused
	case !found || active:
		status = monitorStatusNotPaused
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyWatch: name, cliJSONKeyMonitoring: status})
		return exitSuccess
	}
	switch status {
	case monitorStatusPaused:
		fmt.Fprintf(a.Stdout, "monitoring paused for watch %s\n", name)
	case monitorStatusResumed:
		fmt.Fprintf(a.Stdout, "monitoring resumed for watch %s\n", name)
	default:
		fmt.Fprintf(a.Stdout, "watch %s was not paused\n", name)
	}
	return exitSuccess
}

// knownWatchName reports whether name is a declared watch — a global `watches:`
// entry, or a service-embedded watch "<service>:<watch>". Used to reject typos in
// `watch monitor|unmonitor` (mirroring the web SetWatchMonitored "unknown watch"
// check) rather than silently writing an inert monitor-state key.
func knownWatchName(cfg *config.Config, name string) bool {
	if raw, _ := cfg.ResolveWatches(); raw != nil {
		if _, ok := raw[name]; ok {
			return true
		}
	}
	for _, svc := range cfg.SortedServiceNames() {
		resolved, errs := cfg.Resolve(svc)
		if len(errs) > 0 || resolved.Tree == nil {
			continue
		}
		watches, ok := resolved.Tree[config.SectionWatches].(map[string]any)
		if !ok {
			continue
		}
		for wn := range watches {
			if svc+":"+wn == name {
				return true
			}
		}
	}
	return false
}

func (a App) runWatchStatus(ctx context.Context, opts options) int {
	if len(opts.args) != 2 {
		return a.commandUsageError(commandWatch, "watch status requires exactly one watch name")
	}
	name := opts.args[1]
	state := app.TargetStateOK
	if a.FetchDaemonWatchState != nil {
		if st, ok := a.FetchDaemonWatchState(ctx, opts, name); ok && st != "" {
			state = st
		}
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]string{cliJSONKeyWatch: name, cliJSONKeyState: state})
		return exitSuccess
	}
	fmt.Fprintf(a.Stdout, "%s state=%s\n", name, state)
	return exitSuccess
}
