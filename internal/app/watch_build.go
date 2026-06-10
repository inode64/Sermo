package app

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/rules"
	"sermo/internal/volume"
)

const notifyNone = "none"

// BuildWatches resolves the global `watches` section into runnable Watches, plus
// the per-service version/config monitors synthesized from each service's
// `version:`/`config:` blocks. Disabled or malformed entries are skipped with a
// warning (like BuildWorkers).
func BuildWatches(cfg *config.Config, deps Deps, defaultInterval time.Duration) ([]*Watch, []string) {
	var watches []*Watch
	var warnings []string

	sw, swarn := serviceMonitorWatches(cfg, deps, defaultInterval)
	watches = append(watches, sw...)
	warnings = append(warnings, swarn...)

	raw, ok := cfg.Global.Raw["watches"].(map[string]any)
	if !ok || len(raw) == 0 {
		return watches, warnings
	}

	for _, name := range sortedWatchNames(raw) {
		entry, ok := raw[name].(map[string]any)
		if !ok {
			warnings = append(warnings, "watch "+name+" is not a mapping")
			continue
		}
		if isDisabled(entry) {
			continue
		}

		checkEntry, ok := entry["check"].(map[string]any)
		if !ok {
			warnings = append(warnings, "watch "+name+": missing check")
			continue
		}

		interval := defaultInterval
		if d := durationField(entry["interval"]); d > 0 {
			interval = d
		}

		if w := applyWatchMonitorMode(deps.Monitor, name, config.MonitorMode(entry)); w != "" {
			warnings = append(warnings, w)
		}

		switch cfgval.AsString(checkEntry["type"]) {
		case "net", "icmp", "swap":
			expanded, warns := buildMetricWatches(name, entry, checkEntry, deps, interval)
			watches = append(watches, expanded...)
			warnings = append(warnings, warns...)
		case "file":
			w, warn := buildFileWatch(name, entry, checkEntry, deps, interval)
			if warn != "" {
				warnings = append(warnings, warn)
				continue
			}
			watches = append(watches, w)
		case "process":
			w, warn := buildProcWatch(name, entry, checkEntry, deps, interval)
			if warn != "" {
				warnings = append(warnings, warn)
				continue
			}
			watches = append(watches, w)
		default:
			w, warn := buildSingleWatch(name, entry, checkEntry, deps, interval)
			if warn != "" {
				warnings = append(warnings, warn)
				continue
			}
			watches = append(watches, w)
		}
	}
	return watches, warnings
}

// buildSingleWatch builds the standard one-Watch-per-entry shape: an inline check
// plus the entry's top-level actions. It serves the host-resource checks
// (disk/load/…) and any single-shot service check (tcp/http/…) used as a watch.
// Health checks fire the hook on failure; condition checks on OK (threshold met).
func buildSingleWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	typ := cfgval.AsString(checkEntry["type"])
	check, err := checks.BuildInline(name, checkEntry, checks.Deps{
		DefaultTimeout: deps.DefaultTimeout,
		DiskUsage:      deps.DiskUsage,
		MountSampler:   deps.MountSampler,
	})
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	then, err := thenMap(entry)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	hook, names, err := parseActions(then)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	effectiveNames := effectiveNotify(names, deps.GlobalNotify)
	expand, err := parseExpand(then, typ)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	if len(hook.Command) == 0 && !hasNotifyAction(effectiveNames) && expand == nil {
		return nil, "watch " + name + ": then requires a hook, notify and/or expand"
	}
	w := &Watch{
		Name:       name,
		CheckType:  typ,
		Check:      check,
		Window:     rules.ParseWindowRule(entry),
		Hook:       hook,
		Notifiers:  resolveNotifiers(effectiveNames, deps.Notifiers),
		Runner:     OSHookRunner{Runner: deps.ExecxRunner},
		Interval:   interval,
		IsPaused:   monitorPaused(deps.Monitor, watchMonitorKey(name)),
		FireOnFail: isHealthCheckType(typ),
		Now:        deps.Now,
		Emit:       deps.Emit,
	}
	if expand != nil {
		w.Expand = expand
		w.Policy = rules.ParsePolicy(entry)
		w.Expander = volume.Expander{Runner: deps.ExecxRunner}
	}
	return w, ""
}

// isHealthCheckType reports whether a check type's OK==true means "healthy", so a
// watch over it fires its hook on failure rather than on OK (the alert condition
// for disk/load/metric/count and the other threshold checks).
func isHealthCheckType(typ string) bool {
	return checks.IsHealthType(typ)
}

// buildMetricWatches expands one multi-metric watch entry (net/icmp/swap) into
// one Watch per metric, each with its own check, window and actions. The per-metric
// check entry is the watch's base check fields plus metric:<key> plus the
// metric block's condition keys (everything except then/for/within). Builder-set
// keys (type, host/interface, count, metric) take precedence over the block.
func buildMetricWatches(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) ([]*Watch, []string) {
	metrics, ok := entry["metrics"].(map[string]any)
	if !ok || len(metrics) == 0 {
		return nil, []string{"watch " + name + ": " + cfgval.AsString(checkEntry["type"]) + " check requires a non-empty metrics map"}
	}
	var out []*Watch
	var warns []string
	for _, key := range sortedWatchNames(metrics) {
		mEntry, ok := metrics[key].(map[string]any)
		if !ok {
			warns = append(warns, "watch "+name+".metrics."+key+": not a mapping")
			continue
		}
		ce := map[string]any{}
		for k, v := range mEntry { // condition keys
			switch k {
			case "then", "for", "within":
			default:
				ce[k] = v
			}
		}
		for k, v := range checkEntry { // base check fields win
			ce[k] = v
		}
		ce["metric"] = key

		check, err := checks.BuildInline(name, ce, checks.Deps{
			DefaultTimeout: deps.DefaultTimeout,
			DiskUsage:      deps.DiskUsage,
			MountSampler:   deps.MountSampler,
		})
		if err != nil {
			warns = append(warns, "watch "+name+".metrics."+key+": "+err.Error())
			continue
		}
		hook, names, err := parseThen(mEntry)
		if err != nil {
			warns = append(warns, "watch "+name+".metrics."+key+": "+err.Error())
			continue
		}
		effectiveNames := effectiveNotify(names, deps.GlobalNotify)
		if len(hook.Command) == 0 && !hasNotifyAction(effectiveNames) {
			warns = append(warns, "watch "+name+".metrics."+key+": then requires a hook and/or notify")
			continue
		}
		out = append(out, &Watch{
			Name:      name,
			CheckType: cfgval.AsString(checkEntry["type"]),
			Check:     check,
			Window:    rules.ParseWindowRule(mEntry),
			Hook:      hook,
			Notifiers: resolveNotifiers(effectiveNames, deps.Notifiers),
			Runner:    OSHookRunner{Runner: deps.ExecxRunner},
			Interval:  interval,
			IsPaused:  monitorPaused(deps.Monitor, watchMonitorKey(name)),
			Now:       deps.Now,
			Emit:      deps.Emit,
		})
	}
	return out, warns
}

// buildFileWatch builds a stateful file watch: a fileWatcher (its own per-path
// baseline, conditions and hook) wired into a Watch through Watch.Cycle so it can
// fire one hook per change. The Watch's check/window fields are unused.
func buildFileWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	if cfgval.AsString(checkEntry["path"]) == "" {
		return nil, "watch " + name + ": file check requires a path"
	}
	cond, err := parseFileCond(checkEntry)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	hook, names, err := parseThen(entry)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	effectiveNames := effectiveNotify(names, deps.GlobalNotify)
	if len(hook.Command) == 0 && !hasNotifyAction(effectiveNames) {
		return nil, "watch " + name + ": then requires a hook and/or notify"
	}
	fw := &fileWatcher{
		name:      name,
		path:      cfgval.AsString(checkEntry["path"]),
		recursive: boolField(checkEntry["recursive"]),
		cond:      cond,
		hook:      hook,
		notifiers: resolveNotifiers(effectiveNames, deps.Notifiers),
		runner:    OSHookRunner{Runner: deps.ExecxRunner},
		emit:      deps.Emit,
	}
	return &Watch{
		Name:      name,
		CheckType: "file",
		Interval:  interval,
		IsPaused:  monitorPaused(deps.Monitor, watchMonitorKey(name)),
		Now:       deps.Now,
		Emit:      deps.Emit,
		Cycle:     fw.runCycle,
	}, ""
}

// buildProcWatch builds a stateful process watch: a procWatcher (its own per-PID
// state, conditions and hook) wired into a Watch through Watch.Cycle so it can
// fire one hook per matching PID.
func buildProcWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	pname := cfgval.AsString(checkEntry["name"])
	if pname == "" {
		return nil, "watch " + name + ": process check requires a name"
	}
	cond, err := parseProcCond(checkEntry)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	hook, names, err := parseThen(entry)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	effectiveNames := effectiveNotify(names, deps.GlobalNotify)
	if len(hook.Command) == 0 && !hasNotifyAction(effectiveNames) {
		return nil, "watch " + name + ": then requires a hook and/or notify"
	}
	pw := &procWatcher{
		name:      name,
		match:     ProcMatch{Name: pname, User: cfgval.AsString(checkEntry["user"])},
		cond:      cond,
		hook:      hook,
		notifiers: resolveNotifiers(effectiveNames, deps.Notifiers),
		runner:    OSHookRunner{Runner: deps.ExecxRunner},
		now:       deps.Now,
		emit:      deps.Emit,
		sampler:   deps.ProcSampler, // nil -> osProcSampler at run time
	}
	return &Watch{
		Name:      name,
		CheckType: "process",
		Interval:  interval,
		IsPaused:  monitorPaused(deps.Monitor, watchMonitorKey(name)),
		Now:       deps.Now,
		Emit:      deps.Emit,
		Cycle:     pw.runCycle,
	}, ""
}

// parseProcCond reads the for/cpu/memory/io conditions from a process check
// entry. At least one must be present.
func parseProcCond(check map[string]any) (procCond, error) {
	var c procCond
	if _, present := check["for"]; present {
		d := durationField(check["for"])
		if d <= 0 {
			return c, fmt.Errorf("process for must be a positive duration")
		}
		c.minAge = d
	}
	type thr struct {
		key string
		op  *string
		val *float64
	}
	for _, t := range []thr{
		{"cpu", &c.cpuOp, &c.cpuValue},
		{"memory", &c.memOp, &c.memValue},
		{"io", &c.ioOp, &c.ioValue},
	} {
		m, ok := check[t.key].(map[string]any)
		if !ok {
			continue
		}
		op := cfgval.AsString(m["op"])
		if !validThresholdOp(op) {
			return c, fmt.Errorf("process %s requires a valid op (>=, >, <=, <, ==, !=)", t.key)
		}
		v, ok := floatField(m["value"])
		if !ok {
			return c, fmt.Errorf("process %s value must be numeric", t.key)
		}
		*t.op, *t.val = op, v
	}
	if v, present := check["gone"]; present {
		b, ok := v.(bool)
		if !ok {
			return c, fmt.Errorf("process gone must be a boolean")
		}
		c.onGone = b
	}
	if !c.any() {
		return c, fmt.Errorf("process check requires at least one of for, cpu, memory, io, gone")
	}
	return c, nil
}

// parseFileCond reads the size/permissions/owner/existence conditions from a file
// check entry. At least one must be present.
func parseFileCond(check map[string]any) (fileCond, error) {
	var c fileCond
	if sz, ok := check["size"].(map[string]any); ok {
		if cfgval.AsString(sz["on"]) == "change" {
			c.sizeChange = true
		} else {
			op := cfgval.AsString(sz["op"])
			if !validThresholdOp(op) {
				return c, fmt.Errorf("file size requires on: change or {op, value}")
			}
			v, ok := floatField(sz["value"])
			if !ok {
				return c, fmt.Errorf("file size value must be numeric")
			}
			c.sizeOp, c.sizeValue = op, v
		}
	}
	if p, ok := check["permissions"].(map[string]any); ok {
		if cfgval.AsString(p["on"]) != "change" {
			return c, fmt.Errorf("file permissions requires on: change")
		}
		c.permChange = true
	}
	if o, ok := check["owner"].(map[string]any); ok {
		if cfgval.AsString(o["on"]) != "change" {
			return c, fmt.Errorf("file owner requires on: change")
		}
		c.ownerChange = true
	}
	if e, ok := check["existence"].(map[string]any); ok {
		if cfgval.AsString(e["on"]) != "delete" {
			return c, fmt.Errorf("file existence requires on: delete")
		}
		c.onDelete = true
	}
	if !c.any() {
		return c, fmt.Errorf("file check requires at least one of size, permissions, owner, existence")
	}
	return c, nil
}

// parseThen reads a `then` block into an optional hook and an optional list of
// notifier names. A missing block means no per-watch action was declared; callers
// decide whether a global notify default makes that valid.
func parseThen(entry map[string]any) (HookSpec, []string, error) {
	then, err := thenMap(entry)
	if err != nil {
		return HookSpec{}, nil, err
	}
	if then == nil {
		return HookSpec{}, nil, nil
	}
	return parseActions(then)
}

func thenMap(entry map[string]any) (map[string]any, error) {
	raw, present := entry["then"]
	if !present {
		return nil, nil
	}
	then, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("then must be a mapping")
	}
	return then, nil
}

// parseActions reads the hook and notify targets from a `then` block without
// requiring that at least one is present. The caller enforces action presence
// after applying the global notify default. It errors only on a malformed hook.
func parseActions(then map[string]any) (HookSpec, []string, error) {
	var hook HookSpec
	if h, ok := then["hook"].(map[string]any); ok {
		cmd := cfgval.StringArray(h["command"])
		if len(cmd) == 0 {
			return HookSpec{}, nil, fmt.Errorf("hook requires a non-empty command")
		}
		hook = HookSpec{Command: cmd, Timeout: durationField(h["timeout"])}
		if v, ok := cfgval.Int(h["expect_exit"]); ok {
			hook.ExpectExit = &v
		}
		stdout, warn := checks.ParseOutputMatcher(h["expect_stdout"])
		if warn != "" {
			return HookSpec{}, nil, fmt.Errorf("hook expect_stdout %s", warn)
		}
		stderr, warn := checks.ParseOutputMatcher(h["expect_stderr"])
		if warn != "" {
			return HookSpec{}, nil, fmt.Errorf("hook expect_stderr %s", warn)
		}
		hook.Stdout, hook.Stderr = stdout, stderr
	}
	return hook, cfgval.StringList(then["notify"]), nil
}

// parseExpand reads a `then.expand` disk-expansion action. It is only valid on a
// disk watch, since the action grows the volume backing the checked path.
func parseExpand(then map[string]any, checkType string) (*ExpandSpec, error) {
	raw, ok := then["expand"].(map[string]any)
	if !ok {
		return nil, nil
	}
	if checkType != "disk" {
		return nil, fmt.Errorf("then.expand is only valid on a disk watch, not %q", checkType)
	}
	by := cfgval.AsString(raw["by"])
	if by == "" {
		return nil, fmt.Errorf("then.expand requires a `by` size (e.g. 5G)")
	}
	n, err := parseSize(by)
	if err != nil {
		return nil, fmt.Errorf("then.expand by: %w", err)
	}
	if n <= 0 {
		return nil, fmt.Errorf("then.expand by must be positive")
	}
	return &ExpandSpec{By: n}, nil
}

// resolveNotifiers maps notifier names to the configured notifiers, skipping
// unknown names (config validation reports those; a build-time miss means the
// notifier itself failed to build, already warned by notify.Build).
func resolveNotifiers(names []string, reg map[string]notify.Notifier) []notify.Notifier {
	out := make([]notify.Notifier, 0, len(names))
	for _, n := range names {
		if nt, ok := reg[n]; ok {
			out = append(out, nt)
		}
	}
	return out
}

func hasNotifyAction(names []string) bool {
	return len(names) > 0 && !slices.Contains(names, notifyNone)
}

// serviceMonitorWatches synthesizes the per-service version/config monitors from
// each resolved service's `version:`/`config:` blocks, reusing the profile's
// `commands.version` and `preflight.config`. They are built once (like host
// watches) so their on_change detection persists across cycles.
func serviceMonitorWatches(cfg *config.Config, deps Deps, defaultInterval time.Duration) ([]*Watch, []string) {
	var watches []*Watch
	var warnings []string
	for _, name := range serviceNames(cfg) {
		resolved, errs := cfg.Resolve(name)
		if len(errs) > 0 || resolved.Tree == nil {
			continue
		}
		tree := resolved.Tree
		interval := defaultInterval
		if d := durationField(tree["interval"]); d > 0 {
			interval = d
		}
		for _, m := range []struct {
			suffix string
			build  func(string, map[string]any, Deps, time.Duration) (*Watch, string)
		}{{"version", versionMonitor}, {"config", configMonitor}} {
			w, warn := m.build(name, tree, deps, interval)
			if warn != "" {
				warnings = append(warnings, warn)
			} else if w != nil {
				watches = append(watches, w)
			}
		}
	}
	return watches, warnings
}

// versionMonitor synthesizes a watch that alerts when the service's reported
// version changes, using the profile's version command (preflight.version, then
// commands.version). nil when the service declares no `version.on_change`.
func versionMonitor(name string, tree map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	notify, ok := onChangeNotify(tree["version"])
	if !ok {
		return nil, ""
	}
	cmd := versionCommandRaw(tree)
	if cmd == nil {
		return nil, "service " + name + ": version monitor needs commands.version (or preflight.version) in the profile"
	}
	check, err := checks.BuildInline(name+":version", map[string]any{"type": "command", "command": cmd, "on_change": true}, monitorDeps(deps))
	if err != nil {
		return nil, "service " + name + ": version monitor: " + err.Error()
	}
	return monitorWatch(name+":version", "command", check, notify, deps, interval), ""
}

// configMonitor synthesizes a watch that alerts when the service's config is
// invalid (the profile's preflight.config test fails) or — with a `path` — when a
// config file changes. nil when the service declares no `config.on_change`.
func configMonitor(name string, tree map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	block, _ := tree["config"].(map[string]any)
	notify, ok := onChangeNotify(tree["config"])
	if !ok {
		return nil, ""
	}
	entry := map[string]any{"type": "config", "on_change": true}
	if cmd := configTestCommandRaw(tree); cmd != nil {
		entry["command"] = cmd
	}
	if p, present := block["path"]; present {
		entry["path"] = p
	}
	if entry["command"] == nil && entry["path"] == nil {
		return nil, "service " + name + ": config monitor needs preflight.config (or a path)"
	}
	check, err := checks.BuildInline(name+":config", entry, monitorDeps(deps))
	if err != nil {
		return nil, "service " + name + ": config monitor: " + err.Error()
	}
	return monitorWatch(name+":config", "config", check, notify, deps, interval), ""
}

// onChangeNotify reads an `{on_change: {notify: [...]}}` block, returning the
// notifier names and whether on_change is declared.
func onChangeNotify(v any) ([]string, bool) {
	block, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	oc, ok := block["on_change"].(map[string]any)
	if !ok {
		return nil, false
	}
	return cfgval.StringList(oc["notify"]), true
}

// versionCommandRaw returns the raw version-command argv from the resolved tree
// (preflight.version then commands.version), or nil.
func versionCommandRaw(tree map[string]any) any {
	for _, src := range []string{"preflight", "commands"} {
		if section, ok := tree[src].(map[string]any); ok {
			if entry, ok := section["version"].(map[string]any); ok && entry["command"] != nil {
				return entry["command"]
			}
		}
	}
	return nil
}

// configTestCommandRaw returns the raw config-test argv from preflight.config (the
// profile's, or the service's custom preflight that replaced it), or nil.
func configTestCommandRaw(tree map[string]any) any {
	if pf, ok := tree["preflight"].(map[string]any); ok {
		if entry, ok := pf["config"].(map[string]any); ok {
			return entry["command"]
		}
	}
	return nil
}

// monitorDeps maps the app Deps to the checks.Deps a synthesized monitor needs.
func monitorDeps(deps Deps) checks.Deps {
	return checks.Deps{DefaultTimeout: deps.DefaultTimeout, Runner: deps.ExecxRunner}
}

// monitorWatch assembles a notify-only watch around a synthesized check.
func monitorWatch(name, checkType string, check checks.Check, notify []string, deps Deps, interval time.Duration) *Watch {
	return &Watch{
		Name:       name,
		CheckType:  checkType,
		Check:      check,
		Notifiers:  resolveNotifiers(effectiveNotify(notify, deps.GlobalNotify), deps.Notifiers),
		Runner:     OSHookRunner{Runner: deps.ExecxRunner},
		Interval:   interval,
		IsPaused:   monitorPaused(deps.Monitor, watchMonitorKey(name)),
		FireOnFail: true, // command/config are health-style: alert (notify) on failure/change
		Now:        deps.Now,
		Emit:       deps.Emit,
	}
}

// effectiveNotify applies notify precedence (per-site over global): an explicit
// site selection wins, the `none` sentinel suppresses all delivery, and an
// omitted selection inherits the global default.
func effectiveNotify(site, global []string) []string {
	if slices.Contains(site, notifyNone) {
		return nil
	}
	if len(site) > 0 {
		return site
	}
	return global
}

func sortedWatchNames(m map[string]any) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func durationField(v any) time.Duration {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

func boolField(v any) bool {
	b, _ := v.(bool)
	return b
}

// floatField reads a numeric field that may decode as a YAML int, float or
// string, reporting whether it parsed.
func floatField(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint64:
		return float64(t), true
	case float64:
		return t, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	default:
		return 0, false
	}
}
