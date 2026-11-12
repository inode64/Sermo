package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"sermo/internal/app"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/severity"
	"sermo/internal/state"
	"sermo/internal/web"
)

const (
	watchCommandTargetArgCount = 2
	// watchStateUnknown is reported when sermod cannot supply the watch state.
	watchStateUnknown = "unknown"
)

// runWatch dispatches host-watch queries against the running daemon.
func (a App) runWatch(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError(commandWatch, "watch requires subcommand status, monitor, unmonitor, silence, probe, refresh, kill, pause or resume")
	}
	switch opts.args[0] {
	case commandStatus:
		return a.runWatchStatus(ctx, opts)
	case commandMonitor:
		return a.runWatchMonitor(ctx, opts, false)
	case commandUnmonitor:
		return a.runWatchMonitor(ctx, opts, true)
	case web.APIActionSilence:
		return a.runWatchSilence(ctx, opts)
	case "probe":
		return a.runWatchProbe(ctx, opts)
	case commandArgRefresh:
		// Every enabled watch, or the named ones; the daemon resolves the names.
		return a.runDaemonRefresh(ctx, opts, commandWatch+" "+commandArgRefresh, web.RefreshPath(web.APIPathWatches, opts.args[1:]...))
	case commandWatchKill:
		return a.runWatchKill(ctx, opts)
	case app.RaidControlPause, app.RaidControlResume:
		return a.runWatchRAIDControl(ctx, opts, opts.args[0])
	default:
		return a.commandUsageError(commandWatch, fmt.Sprintf("unknown watch subcommand %q", opts.args[0]))
	}
}

func (a App) runWatchProbe(ctx context.Context, opts options) int {
	if len(opts.args) != watchCommandTargetArgCount {
		return a.commandUsageError(commandWatch, "watch probe requires exactly one watch name")
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	entry, ok := configuredHostWatch(cfg, opts.args[1])
	if !ok {
		return a.fail(opts, fmt.Sprintf("unknown host watch %q", opts.args[1]))
	}
	checkEntry, _ := entry[config.WatchKeyCheck].(map[string]any)
	typ := fmt.Sprint(checkEntry[checks.CheckKeyType])
	if !app.ManualProbeCheckType(typ) {
		return a.fail(opts, fmt.Sprintf("watch %q (%s) does not support manual probing", opts.args[1], typ))
	}
	budget := app.WatchProbeBudget(checkEntry, engineDefaultTimeout(cfg), manualOperationTimeout(cfg, opts))
	probeCtx, cancel := context.WithTimeout(ctx, daemonActionTimeout(opts, budget))
	defer cancel()
	result, err := a.ProbeDaemonWatch(probeCtx, cfg, opts.args[1])
	// A failing sample comes back as a 409 carrying its graded result: print
	// it at its grade. Only a probe that produced no result is a failure of
	// the command itself.
	if err != nil && !severity.Level(result.Severity).Valid() {
		return a.fail(opts, "watch probe: "+err.Error())
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyWatch: opts.args[1], cliJSONKeyOK: result.OK, cliJSONKeyMessage: result.Message, "readings": result.Readings, checks.CheckKeySeverity: result.Severity})
	} else {
		status := cliTextOK
		if !result.OK {
			status = severityLabel(severity.Level(result.Severity))
		}
		fmt.Fprintf(a.Stdout, "%s watch %s: %s\n", status, opts.args[1], result.Message)
		for _, reading := range result.Readings {
			printWatchReading(a.Stdout, reading)
		}
	}
	if result.OK {
		return exitSuccess
	}
	return exitNotActive
}

// probeDaemonWatch reports a 409, a sample that failed, as an error carrying
// the graded result, which runWatchProbe prints at its grade.
func (a App) probeDaemonWatch(ctx context.Context, cfg *config.Config, watch string) (web.ActionResult, error) {
	result, status, err := a.postDaemonAction(ctx, cfg, "probe", web.APIPathWatches+"/"+url.PathEscape(watch)+"/probe")
	if err == nil && status == http.StatusConflict {
		err = fmt.Errorf("probe failed (%d): %s", status, strings.TrimSpace(result.Message))
	}
	return result, err
}

func (a App) runWatchRAIDControl(ctx context.Context, opts options, action string) int {
	if len(opts.args) != watchCommandTargetArgCount {
		return a.commandUsageError(commandWatch, fmt.Sprintf("watch %s requires exactly one watch name", action))
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	entry, ok := configuredHostWatch(cfg, opts.args[1])
	if !ok {
		return a.fail(opts, fmt.Sprintf("unknown host watch %q", opts.args[1]))
	}
	checkEntry, _ := entry[config.WatchKeyCheck].(map[string]any)
	if fmt.Sprint(checkEntry[checks.CheckKeyType]) != checks.CheckTypeRAID {
		return a.fail(opts, fmt.Sprintf("watch %q is not a RAID watch", opts.args[1]))
	}
	control, _ := entry[config.WatchKeyRAIDControl].(map[string]any)
	if !cfgval.Bool(control[config.RAIDControlKeyPauseResume]) {
		return a.fail(opts, fmt.Sprintf("watch %q has no raid_control.pause_resume configured", opts.args[1]))
	}
	array := fmt.Sprint(checkEntry[checks.CheckKeyArray])
	if action == app.RaidControlPause && opts.confirm != array {
		return a.commandUsageError(commandWatch, "watch pause requires --confirm "+array)
	}
	timeout := manualOperationTimeout(cfg, opts)
	reclaims := a.newLockReclaimLog()
	result := app.ControlRAID(ctx, cfg.Global.RuntimeDir(), array, action, timeout, reclaims.watch(opts.args[1]))
	a.flushLockReclaims(ctx, cfg, reclaims)
	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyWatch: opts.args[1], cliJSONKeyOK: result.OK, cliJSONKeyMessage: result.Message})
	} else {
		fmt.Fprintln(a.Stdout, result.Message)
	}
	if result.OK {
		return exitSuccess
	}
	return exitBlocked
}

// manualOperationTimeout keeps manual service operations and RAID control on the
// engine.operation_timeout budget the daemon and web use, unless the operator
// explicitly supplied --timeout. prepareOptions gives every command a CLI
// default, so opts.timeout alone cannot distinguish that default from an
// operator override.
func manualOperationTimeout(cfg *config.Config, opts options) time.Duration {
	if opts.timeoutSet {
		return opts.timeout
	}
	return config.EngineDuration(cfg, config.EngineKeyOperationTimeout, app.DefaultEngineOperationTimeout)
}

// daemonActionTimeout bounds a request that waits for work the daemon runs under
// budget: the operator's --timeout, or that budget plus the time the daemon needs
// to answer once the work ends.
func daemonActionTimeout(opts options, budget time.Duration) time.Duration {
	if opts.timeoutSet {
		return opts.timeout
	}
	return budget + daemonActionAnswerMargin
}

func configuredHostWatch(cfg *config.Config, name string) (map[string]any, bool) {
	raw, _ := cfg.ResolveWatches()
	entry, ok := raw[name].(map[string]any)
	return entry, ok
}

// runWatchMonitor pauses (`unmonitor`) or resumes (`monitor`) a single watch by
// its name — a host watch or a service-embedded watch ("<service>:<watch>"). The
// state persists under engine.state keyed independently of any service, so
// unmonitoring a service never touches its watches and vice versa. The daemon
// reads this key live each cycle.
func (a App) runWatchMonitor(ctx context.Context, opts options, pause bool) int {
	verb := monitorVerb(pause)
	if len(opts.args) != watchCommandTargetArgCount {
		return a.commandUsageError(commandWatch, fmt.Sprintf("watch %s requires exactly one watch name", verb))
	}
	name := opts.args[1]

	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	if !knownWatchName(cfg, name) {
		return a.fail(opts, fmt.Sprintf("unknown watch %q", name))
	}
	failPrefix := "watch " + verb
	return a.applyMonitorTransition(ctx, opts, cfg, app.WatchMonitorKey(name), failPrefix, pause, nil,
		func(_ *state.Store, status string) int {
			if opts.json {
				writeJSON(a.Stdout, map[string]any{cliJSONKeyWatch: name, cliJSONKeyMonitoring: status})
				return exitSuccess
			}
			a.printMonitorStatus("watch "+name, status, "")
			return exitSuccess
		},
	)
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
	// Only a service the name starts with can own it, so only those resolve.
	for _, svc := range cfg.SortedServiceNames() {
		wn, ok := strings.CutPrefix(name, svc+":")
		if !ok {
			continue
		}
		resolved, errs := cfg.Resolve(svc)
		if len(errs) > 0 || resolved.Tree == nil {
			continue
		}
		watches, _ := resolved.Tree[config.SectionWatches].(map[string]any)
		if _, ok := watches[wn]; ok {
			return true
		}
	}
	return false
}

func (a App) runWatchStatus(ctx context.Context, opts options) int {
	if len(opts.args) != watchCommandTargetArgCount {
		return a.commandUsageError(commandWatch, "watch status requires exactly one watch name")
	}
	name := opts.args[1]
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	if !knownWatchName(cfg, name) {
		return a.fail(opts, fmt.Sprintf("unknown watch %q", name))
	}
	// Only the daemon observes watches. Without its answer the state is
	// unknown, never ok: a monitoring script must not read a stopped daemon
	// or a rejected token as a healthy watch.
	watchState, code := watchStateUnknown, exitRuntimeError
	detail, ok := a.FetchDaemonWatchDetail(ctx, cfg, name)
	if ok && detail.State != "" {
		watchState, code = detail.State, exitSuccess
	} else if !opts.quiet {
		fmt.Fprintf(a.Stderr, "warning: watch %s: sermod did not report its state (daemon stopped or web API unavailable)\n", name)
	}
	if opts.json {
		out := map[string]any{cliJSONKeyWatch: name, cliJSONKeyState: watchState}
		if detail.Alarm.Revision != "" {
			out["alarm"] = detail.Alarm
		}
		if detail.LastCheckedAt != "" {
			out["last_checked_at"] = detail.LastCheckedAt
		}
		if detail.NextCheckAt != "" {
			out["next_check_at"] = detail.NextCheckAt
		}
		if len(detail.Readings) > 0 {
			out["readings"] = detail.Readings
		}
		writeJSON(a.Stdout, out)
		return code
	}
	fmt.Fprintf(a.Stdout, "%s state=%s\n", name, watchState)
	if detail.Alarm.Silenced {
		fmt.Fprintln(a.Stdout, "  Alarm: silenced until the watch state changes")
	}
	if detail.LastCheckedAt != "" {
		fmt.Fprintf(a.Stdout, "  Last checked: %s\n", detail.LastCheckedAt)
	}
	if detail.NextCheckAt != "" {
		fmt.Fprintf(a.Stdout, "  Next check: %s\n", detail.NextCheckAt)
	}
	for _, reading := range detail.Readings {
		printWatchReading(a.Stdout, reading)
	}
	return code
}

func printWatchReading(out io.Writer, reading web.WatchReading) {
	label := reading.Label
	if label == "" {
		label = reading.Field
	}
	// A reading reports either a value or the reason it has none; an advisory
	// puts that reason in Warning rather than Error, and both read the same here.
	value := cmp.Or(reading.Warning, reading.Error, reading.Value)
	if label != "" && value != "" {
		fmt.Fprintf(out, "  %s: %s\n", label, value)
	}
}

func (a App) runWatchSilence(ctx context.Context, opts options) int {
	if len(opts.args) != watchCommandTargetArgCount {
		return a.commandUsageError(commandWatch, "watch silence requires exactly one watch name")
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	name := opts.args[1]
	if !knownWatchName(cfg, name) {
		return a.fail(opts, fmt.Sprintf("unknown watch %q", name))
	}
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	detail, ok := a.FetchDaemonWatchDetail(ctx, cfg, name)
	if !ok {
		return a.fail(opts, "watch silence: sermod did not report its state")
	}
	if !detail.Alarm.Active || detail.Alarm.Revision == "" {
		return a.fail(opts, "watch silence: watch has no active alarm")
	}
	path := web.APIPathWatches + "/" + url.PathEscape(name) + "/" + web.APIActionSilence + "?" + web.APIQueryRevision + "=" + url.QueryEscape(detail.Alarm.Revision)
	result, status, err := a.postDaemonAction(ctx, cfg, "watch silence", path)
	if err != nil {
		return a.fail(opts, "watch silence: "+err.Error())
	}
	if opts.json {
		writeJSON(a.Stdout, result)
	} else {
		fmt.Fprintln(a.Stdout, result.Message)
	}
	if !result.OK || status != http.StatusOK {
		return exitBlocked
	}
	return exitSuccess
}
