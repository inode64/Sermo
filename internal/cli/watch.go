package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"sermo/internal/app"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/state"
)

type daemonWatchReading struct {
	Field string `json:"field"`
	Label string `json:"label"`
	Value string `json:"value"`
	Error string `json:"error"`
}

type daemonWatchDetail struct {
	Name          string               `json:"name"`
	State         string               `json:"state"`
	CheckType     string               `json:"check_type"`
	LastCheckedAt string               `json:"last_checked_at"`
	Readings      []daemonWatchReading `json:"readings"`
}

type daemonWatchProbe struct {
	OK       bool                 `json:"ok"`
	Message  string               `json:"message"`
	Readings []daemonWatchReading `json:"readings"`
}

const watchCommandTargetArgCount = 2

// runWatch dispatches host-watch queries against the running daemon.
func (a App) runWatch(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError(commandWatch, "watch requires subcommand status, monitor, unmonitor, probe, pause or resume")
	}
	switch opts.args[0] {
	case commandStatus:
		return a.runWatchStatus(ctx, opts)
	case commandMonitor:
		return a.runWatchMonitor(ctx, opts, false)
	case commandUnmonitor:
		return a.runWatchMonitor(ctx, opts, true)
	case "probe":
		return a.runWatchProbe(ctx, opts)
	case "pause", commandResume:
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
	if code != exitSuccess {
		return code
	}
	entry, ok := configuredHostWatch(cfg, opts.args[1])
	if !ok {
		return a.fail(opts, fmt.Sprintf("unknown host watch %q", opts.args[1]))
	}
	checkEntry, _ := entry[config.WatchKeyCheck].(map[string]any)
	typ := fmt.Sprint(checkEntry[checks.CheckKeyType])
	if typ != checks.CheckTypeHdparm && typ != checks.CheckTypeLVM && typ != checks.CheckTypeRAID && typ != checks.CheckTypeSmart {
		return a.fail(opts, fmt.Sprintf("watch %q (%s) does not support manual probing", opts.args[1], typ))
	}
	result, err := a.ProbeDaemonWatch(ctx, opts, opts.args[1])
	if err != nil {
		return a.fail(opts, "watch probe: "+err.Error())
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyWatch: opts.args[1], cliJSONKeyOK: result.OK, cliJSONKeyMessage: result.Message, "readings": result.Readings})
	} else {
		status := cliTextOK
		if !result.OK {
			status = cliTextFail
		}
		fmt.Fprintf(a.Stdout, "%s watch %s: %s\n", status, opts.args[1], result.Message)
		for _, reading := range result.Readings {
			printWatchReading(a.Stdout, reading.Field, reading.Label, reading.Value, reading.Error)
		}
	}
	if result.OK {
		return exitSuccess
	}
	return exitNotActive
}

func (a App) probeDaemonWatch(ctx context.Context, opts options, watch string) (daemonWatchProbe, error) {
	cfg, code := a.loadConfig(opts)
	if code != exitSuccess || cfg == nil {
		return daemonWatchProbe{}, errors.New("failed to load config")
	}
	base, err := webAPIBase(cfg)
	if err != nil {
		return daemonWatchProbe{}, err
	}
	path := daemonAPIPathWatches + "/" + url.PathEscape(watch) + "/probe"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, nil)
	if err != nil {
		return daemonWatchProbe{}, fmt.Errorf("build probe request: %w", err)
	}
	req.Header.Set(daemonWebCSRFHeader, daemonWebCSRFValue)
	applyDaemonWebAuth(req, cfg)
	client := &http.Client{Timeout: daemonWebClientTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return daemonWatchProbe{}, fmt.Errorf("talking to daemon web UI: %w (is sermod running with web.port set?)", err)
	}
	defer resp.Body.Close()
	var result daemonWatchProbe
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return daemonWatchProbe{}, fmt.Errorf("decode probe response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if result.Message == "" {
			result.Message = resp.Status
		}
		return result, fmt.Errorf("probe failed (%d): %s", resp.StatusCode, strings.TrimSpace(result.Message))
	}
	return result, nil
}

func (a App) runWatchRAIDControl(ctx context.Context, opts options, action string) int {
	if len(opts.args) != watchCommandTargetArgCount {
		return a.commandUsageError(commandWatch, fmt.Sprintf("watch %s requires exactly one watch name", action))
	}
	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
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
	if !configBool(control, config.RAIDControlKeyPauseResume) {
		return a.fail(opts, fmt.Sprintf("watch %q has no raid_control.pause_resume configured", opts.args[1]))
	}
	array := fmt.Sprint(checkEntry[checks.CheckKeyArray])
	if action == "pause" && opts.confirm != array {
		return a.commandUsageError(commandWatch, "watch pause requires --confirm "+array)
	}
	timeout := app.EngineDuration(cfg, config.EngineKeyOperationTimeout, app.DefaultEngineOperationTimeout)
	if opts.timeout > 0 {
		timeout = opts.timeout
	}
	result := app.ControlRAID(ctx, cfg.Global.RuntimeDir(), array, action, timeout)
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

func configuredHostWatch(cfg *config.Config, name string) (map[string]any, bool) {
	raw, _ := cfg.ResolveWatches()
	entry, ok := raw[name].(map[string]any)
	return entry, ok
}

func configBool(entry map[string]any, key string) bool {
	v, _ := entry[key].(bool)
	return v
}

// runWatchMonitor pauses (`unmonitor`) or resumes (`monitor`) a single watch by
// its name — a host watch or a service-embedded watch ("<service>:<watch>"). The
// state persists under paths.state keyed independently of any service, so
// unmonitoring a service never touches its watches and vice versa. The daemon
// reads this key live each cycle.
func (a App) runWatchMonitor(ctx context.Context, opts options, pause bool) int {
	verb := commandMonitor
	if pause {
		verb = commandUnmonitor
	}
	if len(opts.args) != watchCommandTargetArgCount {
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
	store, err := state.OpenContext(ctx, filepath.Join(cfg.Global.StateDir(), state.Filename))
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
	if len(opts.args) != watchCommandTargetArgCount {
		return a.commandUsageError(commandWatch, "watch status requires exactly one watch name")
	}
	name := opts.args[1]
	watchState := app.TargetStateOK
	var detail daemonWatchDetail
	if a.FetchDaemonWatchDetail != nil {
		if current, ok := a.FetchDaemonWatchDetail(ctx, opts, name); ok {
			detail = current
			if detail.State != "" {
				watchState = detail.State
			}
		}
	}
	if a.FetchDaemonWatchState != nil {
		if st, ok := a.FetchDaemonWatchState(ctx, opts, name); ok && st != "" {
			watchState = st
		}
	}
	if opts.json {
		out := map[string]any{cliJSONKeyWatch: name, cliJSONKeyState: watchState}
		if detail.LastCheckedAt != "" {
			out["last_checked_at"] = detail.LastCheckedAt
		}
		if len(detail.Readings) > 0 {
			out["readings"] = detail.Readings
		}
		writeJSON(a.Stdout, out)
		return exitSuccess
	}
	fmt.Fprintf(a.Stdout, "%s state=%s\n", name, watchState)
	if detail.LastCheckedAt != "" {
		fmt.Fprintf(a.Stdout, "  Last checked: %s\n", detail.LastCheckedAt)
	}
	for _, reading := range detail.Readings {
		printWatchReading(a.Stdout, reading.Field, reading.Label, reading.Value, reading.Error)
	}
	return exitSuccess
}

func printWatchReading(out io.Writer, field, label, value, errText string) {
	if label == "" {
		label = field
	}
	if errText != "" {
		value = errText
	}
	if label != "" && value != "" {
		fmt.Fprintf(out, "  %s: %s\n", label, value)
	}
}
