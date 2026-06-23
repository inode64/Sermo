package app

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/notify"
	"sermo/internal/rules"
	"sermo/internal/volume"
)

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

	raw, resErrs := cfg.ResolveWatches() // ${var}-expanded (custom globals + host builtins)
	warnings = append(warnings, resErrs...)
	if len(raw) == 0 {
		return watches, warnings
	}

	for _, name := range slices.Sorted(maps.Keys(raw)) {
		entry, ok := raw[name].(map[string]any)
		if !ok {
			warnings = append(warnings, "watch "+name+" is not a mapping")
			continue
		}
		if cfgval.Disabled(entry) {
			continue
		}

		checkEntry, ok := entry["check"].(map[string]any)
		if !ok {
			warnings = append(warnings, "watch "+name+": missing check")
			continue
		}

		interval := defaultInterval
		if d := cfgval.Duration(entry["interval"]); d > 0 {
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
// (storage/load/…) and any single-shot service check (tcp/http/…) used as a watch.
// Health checks fire the hook on failure; condition checks on OK (threshold met).
func buildSingleWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	typ := cfgval.AsString(checkEntry["type"])
	check, err := checks.BuildInline(name, checkEntry, watchInlineDeps(deps))
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	actions, err := resolveWatchActions(entry, deps, watchActionOptions{
		checkType:    typ,
		parseExpand:  true,
		emptyMessage: "then requires a hook, notify and/or expand",
	})
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	w := &Watch{
		Name:           name,
		CheckType:      typ,
		Check:          check,
		Window:         rules.ParseWindowRule(entry),
		Hook:           actions.hook,
		Notifiers:      resolveNotifiers(actions.effectiveNames, deps.Notifiers),
		NotifyInterval: actions.notifyInterval,
		DryRun:         actions.dryRun,
		Runner:         OSHookRunner{Runner: deps.ExecxRunner},
		Interval:       interval,
		IsPaused:       monitorPaused(deps.Monitor, watchMonitorKey(name)),
		InPanic:        deps.Panic.Active,
		Settling:       deps.Settling,
		FireOnFail:     checks.IsHealthType(typ),
		Now:            deps.Now,
		Emit:           deps.Emit,
	}
	if actions.expand != nil {
		w.Expand = actions.expand
		w.Policy = rules.ParsePolicy(entry)
		w.Expander = configuredVolumeExpander(deps)
	}
	return w, ""
}

func configuredVolumeExpander(deps Deps) VolumeExpander {
	if deps.VolumeExpander != nil {
		return deps.VolumeExpander
	}
	runner := deps.ExecxRunner
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	return volume.Expander{Runner: runner}
}

func hasWatchAction(hook HookSpec, names, effectiveNames []string, expand *ExpandSpec) bool {
	return len(hook.Command) > 0 || config.HasNotifyAction(effectiveNames) || expand != nil || config.NotifyOptedOut(names)
}

func dryRunEnabled(then map[string]any) bool {
	return then != nil && cfgval.Bool(then["dry_run"])
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
	for _, key := range slices.Sorted(maps.Keys(metrics)) {
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

		check, err := checks.BuildInline(name, ce, watchInlineDeps(deps))
		if err != nil {
			warns = append(warns, "watch "+name+".metrics."+key+": "+err.Error())
			continue
		}
		actions, err := resolveWatchActions(mEntry, deps, watchActionOptions{
			emptyMessage: "then requires a hook and/or notify",
		})
		if err != nil {
			warns = append(warns, "watch "+name+".metrics."+key+": "+err.Error())
			continue
		}
		out = append(out, &Watch{
			Name:           name,
			CheckType:      cfgval.AsString(checkEntry["type"]),
			Check:          check,
			Window:         rules.ParseWindowRule(mEntry),
			Hook:           actions.hook,
			Notifiers:      resolveNotifiers(actions.effectiveNames, deps.Notifiers),
			NotifyInterval: actions.notifyInterval,
			DryRun:         actions.dryRun,
			Runner:         OSHookRunner{Runner: deps.ExecxRunner},
			Interval:       interval,
			IsPaused:       monitorPaused(deps.Monitor, watchMonitorKey(name)),
			Now:            deps.Now,
			Emit:           deps.Emit,
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
	actions, err := resolveWatchActions(entry, deps, watchActionOptions{
		emptyMessage: "then requires a hook and/or notify",
	})
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	fw := &fileWatcher{
		name:      name,
		path:      cfgval.AsString(checkEntry["path"]),
		recursive: cfgval.Bool(checkEntry["recursive"]),
		cond:      cond,
		hook:      actions.hook,
		notifiers: resolveNotifiers(actions.effectiveNames, deps.Notifiers),
		dryRun:    actions.dryRun,
		inPanic:   deps.Panic.Active,
		runner:    OSHookRunner{Runner: deps.ExecxRunner},
		emit:      deps.Emit,
	}
	return &Watch{
		Name:      name,
		CheckType: "file",
		Interval:  interval,
		IsPaused:  monitorPaused(deps.Monitor, watchMonitorKey(name)),
		InPanic:   deps.Panic.Active,
		Settling:  deps.Settling,
		DryRun:    actions.dryRun,
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
	actions, err := resolveWatchActions(entry, deps, watchActionOptions{
		emptyMessage: "then requires a hook and/or notify",
	})
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	pw := &procWatcher{
		name:      name,
		match:     ProcMatch{Name: pname, User: cfgval.AsString(checkEntry["user"])},
		cond:      cond,
		hook:      actions.hook,
		notifiers: resolveNotifiers(actions.effectiveNames, deps.Notifiers),
		dryRun:    actions.dryRun,
		inPanic:   deps.Panic.Active,
		runner:    OSHookRunner{Runner: deps.ExecxRunner},
		now:       deps.Now,
		emit:      deps.Emit,
		sampler:   procSamplerFromDeps(deps),
	}
	return &Watch{
		Name:      name,
		CheckType: "process",
		Interval:  interval,
		IsPaused:  monitorPaused(deps.Monitor, watchMonitorKey(name)),
		InPanic:   deps.Panic.Active,
		Settling:  deps.Settling,
		DryRun:    actions.dryRun,
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
		d := cfgval.Duration(check["for"])
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
		if !cfgval.IsCompareOp(op) {
			return c, fmt.Errorf("process %s requires a valid op (>=, >, <=, <, ==, !=)", t.key)
		}
		v, ok := cfgval.Float(m["value"])
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
			if !cfgval.IsCompareOp(op) {
				return c, fmt.Errorf("file size requires on: change or {op, value}")
			}
			v, ok := cfgval.Float(sz["value"])
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

// parseThenAndExplicit reads the (optional) `then` block and returns the hook +
// notifier names, plus the raw then-block map (non-nil iff an explicit `then:` key
// was present and was a valid mapping). Callers use the presence of the then-block
// to decide whether to allow global notify inheritance or force pure monitor/alert
// behavior (no actions, no inheritance).
//
// This removes the previous need for every call site to re-invoke thenMap just to
// test presence (the source of the duplicated if/else blocks).
func parseThenAndExplicit(entry map[string]any) (HookSpec, []string, map[string]any, error) {
	then, err := thenMap(entry)
	if err != nil {
		return HookSpec{}, nil, nil, err
	}
	if then == nil {
		return HookSpec{}, nil, nil, nil
	}
	hook, names, err := parseActions(then)
	return hook, names, then, err
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
		hook = HookSpec{Command: cmd, Timeout: cfgval.Duration(h["timeout"])}
		if v, ok := cfgval.IntList(h["expect_exit"]); ok {
			hook.ExpectExit = v
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

type watchActions struct {
	hook           HookSpec
	effectiveNames []string
	dryRun         bool
	expand         *ExpandSpec
	notifyInterval time.Duration
}

type watchActionOptions struct {
	checkType    string
	parseExpand  bool
	emptyMessage string
}

func resolveWatchActions(entry map[string]any, deps Deps, opts watchActionOptions) (watchActions, error) {
	hook, names, thenBlock, err := parseThenAndExplicit(entry)
	if err != nil {
		return watchActions{}, err
	}
	if thenBlock == nil {
		return watchActions{}, nil
	}
	effectiveNames := effectiveNotify(names, deps.GlobalNotify)
	var expand *ExpandSpec
	if opts.parseExpand {
		expand, err = parseExpand(thenBlock, opts.checkType)
		if err != nil {
			return watchActions{}, err
		}
	}
	if !hasWatchAction(hook, names, effectiveNames, expand) {
		return watchActions{}, errors.New(opts.emptyMessage)
	}
	return watchActions{
		hook:           hook,
		effectiveNames: effectiveNames,
		dryRun:         dryRunEnabled(thenBlock),
		expand:         expand,
		notifyInterval: cfgval.Duration(thenBlock["notify_interval"]),
	}, nil
}

// parseExpand reads a `then.expand` storage-expansion action. It is only valid on
// a storage watch, since the action grows the volume backing the checked path.
func parseExpand(then map[string]any, checkType string) (*ExpandSpec, error) {
	raw, ok := then["expand"].(map[string]any)
	if !ok {
		return nil, nil
	}
	if !isStorageCheckType(checkType) {
		return nil, fmt.Errorf("then.expand is only valid on a storage watch, not %q", checkType)
	}
	// The same grammar as every *_bytes threshold: an explicit size suffix is
	// required so a raw byte count is never confused with another unit.
	n, ok := cfgval.ByteSize(raw["by"])
	if !ok || n == 0 || n > math.MaxInt64 {
		return nil, fmt.Errorf("then.expand requires a positive `by` size with a K/M/G/T suffix (e.g. 5G)")
	}
	return &ExpandSpec{By: int64(n)}, nil
}

func isStorageCheckType(typ string) bool {
	return typ == "storage"
}

// resolveNotifiers maps notifier names to the configured notifiers, skipping
// unknown names (config validation reports those; a build-time miss means the
// notifier itself failed to build, already warned by notify.Build).
func resolveNotifiers(names []string, reg map[string]notify.Notifier) []notify.Notifier {
	out := make([]notify.Notifier, 0, len(names))
	hasWall := false
	for _, n := range names {
		if nt, ok := reg[n]; ok && nt.Type() == "wall" {
			hasWall = true
			break
		}
	}
	for _, n := range names {
		if nt, ok := reg[n]; ok {
			if hasWall && nt.Type() == "tty" {
				continue
			}
			out = append(out, nt)
		}
	}
	return out
}

// serviceMonitorWatches synthesizes the per-service version/config monitors from
// each resolved service's `version:`/`config:` blocks, reusing the daemon's
// `commands.version` and `preflight.config`. They are built once (like host
// watches) so their on_change detection persists across cycles.
func serviceMonitorWatches(cfg *config.Config, deps Deps, defaultInterval time.Duration) ([]*Watch, []string) {
	var watches []*Watch
	var warnings []string
	for _, name := range cfg.SortedServiceNames() {
		resolved, errs := cfg.Resolve(name)
		if len(errs) > 0 || resolved.Tree == nil {
			continue
		}
		tree := resolved.Tree
		interval := defaultInterval
		if d := cfgval.Duration(tree["interval"]); d > 0 {
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
// version changes, using the daemon's version command (preflight.version, then
// commands.version). nil when the service declares no `version.on_change`.
func versionMonitor(name string, tree map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	notify, ok := onChangeNotify(tree["version"])
	if !ok {
		return nil, ""
	}
	entry := versionCommandEntry(tree)
	if entry == nil {
		return nil, "service " + name + ": version monitor needs commands.version (or preflight.version) in the daemon"
	}
	entry["type"] = "command"
	entry["on_change"] = true
	check, err := checks.BuildInline(name+":version", entry, monitorDeps(deps))
	if err != nil {
		return nil, "service " + name + ": version monitor: " + err.Error()
	}
	return monitorWatch(name+":version", "command", check, notify, deps, interval), ""
}

// configMonitor synthesizes a watch that alerts when the service's config is
// invalid (the daemon's preflight.config test fails) or — with a `path` — when a
// config file changes. nil when the service declares no `config.on_change`.
func configMonitor(name string, tree map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	block, _ := tree["config"].(map[string]any)
	notify, ok := onChangeNotify(tree["config"])
	if !ok {
		return nil, ""
	}
	entry := map[string]any{"type": "config", "on_change": true}
	if cmdEntry := configTestCommandEntry(tree); cmdEntry != nil {
		if cmd := cmdEntry["command"]; cmd != nil {
			entry["command"] = cmd
		}
		if user := cmdEntry["user"]; user != nil {
			entry["user"] = user
		}
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

// versionCommandEntry returns a copy of the resolved version-command entry
// (preflight.version then commands.version, via the shared resolver), or nil.
func versionCommandEntry(tree map[string]any) map[string]any {
	if entry := checks.VersionCommandEntry(tree, "version"); entry != nil {
		return maps.Clone(entry)
	}
	return nil
}

// configTestCommandEntry returns a copy of preflight.config (the daemon's, or
// the service's custom preflight that replaced it), or nil.
func configTestCommandEntry(tree map[string]any) map[string]any {
	if pf, ok := tree["preflight"].(map[string]any); ok {
		if entry, ok := pf["config"].(map[string]any); ok {
			return maps.Clone(entry)
		}
	}
	return nil
}

// watchInlineDeps maps the app-level Deps (from daemon) to the checks.Deps
// subset required for checks.BuildInline calls in host-watch construction
// (buildSingleWatch, buildMetricWatches). It forwards every sampler that host
// resource checks and multi-metric watches may use.
func watchInlineDeps(deps Deps) checks.Deps {
	return checkDepsFromAppDeps(deps, checks.Deps{
		DefaultTimeout: deps.DefaultTimeout,
		Runner:         deps.ExecxRunner,
	})
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
	if slices.Contains(site, config.NotifyNone) {
		return nil
	}
	if len(site) > 0 {
		return site
	}
	return global
}
