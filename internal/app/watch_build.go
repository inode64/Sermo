package app

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/emission"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/volume"
)

const (
	defaultWatchKillTermTimeout = 10 * time.Second
	defaultWatchKillTimeout     = 5 * time.Second
	serviceWatchNameSeparator   = ":"
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
		ws, warns := buildWatchEntry(name, entry, deps, defaultInterval)
		watches = append(watches, ws...)
		warnings = append(warnings, warns...)
	}
	return watches, warnings
}

// buildWatchEntry builds the Watch(es) for one watch entry, shared by host
// watches (BuildWatches) and service-embedded watches (serviceWatches). It skips
// a disabled entry, resolves the interval and monitor mode, then dispatches on
// the check type: net/icmp/swap expand to one Watch per metric, file/process are
// stateful watchers, and everything else is a single check-backed Watch. The
// inline check is scoped by deps.WatchCheckDeps when set (service watches).
func buildWatchEntry(name string, entry map[string]any, deps Deps, defaultInterval time.Duration) ([]*Watch, []string) {
	if cfgval.Disabled(entry) {
		return nil, nil
	}
	checkEntry, ok := entry[config.WatchKeyCheck].(map[string]any)
	if !ok {
		return nil, []string{"watch " + name + ": missing check"}
	}
	interval := defaultInterval
	if d := cfgval.Duration(entry[config.EntryKeyInterval]); d > 0 {
		interval = d
	}
	var warnings []string
	if w := applyWatchMonitorMode(deps.Monitor, name, config.MonitorMode(entry)); w != "" {
		warnings = append(warnings, w)
	}
	switch cfgval.AsString(checkEntry[checks.CheckKeyType]) {
	case checks.CheckTypeNet, checks.CheckTypeICMP, checks.CheckTypeSwap:
		expanded, warns := buildMetricWatches(name, entry, checkEntry, deps, interval)
		return expanded, append(warnings, warns...)
	case checks.CheckTypeFile:
		return watchOrWarn(buildFileWatch(name, entry, checkEntry, deps, interval))(warnings)
	case checks.CheckTypeProcess:
		return watchOrWarn(buildProcWatch(name, entry, checkEntry, deps, interval))(warnings)
	default:
		return watchOrWarn(buildSingleWatch(name, entry, checkEntry, deps, interval))(warnings)
	}
}

// watchOrWarn folds a single-watch builder's (watch, warn) result into the
// ([]*Watch, []string) shape buildWatchEntry returns, appending to the entry's
// accumulated warnings.
func watchOrWarn(w *Watch, warn string) func([]string) ([]*Watch, []string) {
	return func(warnings []string) ([]*Watch, []string) {
		if warn != "" {
			return nil, append(warnings, warn)
		}
		return []*Watch{w}, warnings
	}
}

// serviceWatches builds the watches declared inside a service tree's `watches:`
// section. Each runs the host-watch runtime (hook/notify/for-within/dry-run) with
// the service's scoped check deps, so process-scoped checks count only the
// service's PID tree and a `metric` check reads a dedicated per-watch collector
// (newMetricSource) scoped to that tree. Watches are named "<service>:<watch>".
// The host-scoped multi-metric (net/icmp/swap) and kill-capable `process` watch
// types are rejected here (see unsupportedServiceWatchType).
func serviceWatches(service string, tree map[string]any, checkDeps checks.Deps, newMetricSource func() checks.MetricReader, deps Deps, defaultInterval time.Duration) ([]*Watch, []string) {
	section, ok := tree[config.SectionWatches].(map[string]any)
	if !ok || len(section) == 0 {
		return nil, nil
	}
	interval := defaultInterval
	if d := cfgval.Duration(tree[config.EntryKeyInterval]); d > 0 {
		interval = d
	}
	var watches []*Watch
	var warnings []string
	for _, wn := range slices.Sorted(maps.Keys(section)) {
		entry, ok := section[wn].(map[string]any)
		if !ok {
			warnings = append(warnings, "service "+service+": watch "+wn+" is not a mapping")
			continue
		}
		if reservedServiceWatchName(wn) {
			warnings = append(warnings, "service "+service+": watch "+wn+": name is reserved for the version/config monitor; rename it")
			continue
		}
		if warn := unsupportedServiceWatchType(entry); warn != "" {
			warnings = append(warnings, "service "+service+": watch "+wn+": "+warn)
			continue
		}
		// A metric check reads a dedicated per-watch collector so its rate deltas
		// stay isolated from the engine's sampling and from other watches.
		entryDeps := checkDeps
		if isMetricWatch(entry) {
			if newMetricSource == nil {
				warnings = append(warnings, "service "+service+": watch "+wn+": metric source unavailable")
				continue
			}
			entryDeps.Metrics = newMetricSource()
		}
		sd := deps
		sd.WatchCheckDeps = &entryDeps
		ws, warns := buildWatchEntry(service+":"+wn, entry, sd, interval)
		watches = append(watches, ws...)
		warnings = append(warnings, warns...)
	}
	return watches, warnings
}

// isMetricWatch reports whether a watch entry's check is a metric check.
func isMetricWatch(entry map[string]any) bool {
	checkEntry, _ := entry[config.WatchKeyCheck].(map[string]any)
	return cfgval.AsString(checkEntry[checks.CheckKeyType]) == checks.CheckTypeMetric
}

// reservedServiceWatchName reports whether a service watch name collides with a
// synthesized per-service monitor ("<service>:version" / "<service>:config").
// Sharing the name would share the settling and monitor-pause keys, so those
// names are reserved.
func reservedServiceWatchName(name string) bool {
	return name == config.ServiceMonitorKeyVersion || name == config.ServiceMonitorKeyConfig
}

func serviceMonitorWatchName(service, monitor string) string {
	return service + serviceWatchNameSeparator + monitor
}

// unsupportedServiceWatchType reports why a check type cannot back a service
// watch, or "" when it can. net/icmp/swap are host/network multi-metric watches
// that belong in the global watches: section. The `process` watch matches
// processes host-wide by name/user and can kill them, which is unsafe from a
// service scope — use the PID-tree-scoped `process_count`/`metric` types (or a
// host watch) instead. (metric IS supported: it reads a dedicated per-watch
// collector scoped to the service PID tree.)
func unsupportedServiceWatchType(entry map[string]any) string {
	checkEntry, _ := entry[config.WatchKeyCheck].(map[string]any)
	switch cfgval.AsString(checkEntry[checks.CheckKeyType]) {
	case checks.CheckTypeNet, checks.CheckTypeICMP, checks.CheckTypeSwap:
		return "net/icmp/swap watches are host-scoped; declare them under the global watches: section"
	case checks.CheckTypeProcess:
		return "the process watch matches host-wide (and can kill); use process_count or metric for service-scoped process monitoring, or declare a host watch"
	}
	return ""
}

// buildSingleWatch builds the standard one-Watch-per-entry shape: an inline check
// plus the entry's top-level actions. It serves the host-resource checks
// (storage/load/…) and any single-shot service check (tcp/http/…) used as a watch.
// Health checks fire the hook on failure; condition checks on OK (threshold met).
func buildSingleWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	typ := cfgval.AsString(checkEntry[checks.CheckKeyType])
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
		Emission:       emission.Merge(entry[emission.Section], deps.GlobalEmission),
		DryRun:         config.DryRun(entry),
		Runner:         OSHookRunner{Runner: deps.ExecxRunner},
		Interval:       interval,
		IsPaused:       monitorPaused(deps.Monitor, watchMonitorKey(name)),
		InPanic:        deps.Panic.Active,
		Settling:       deps.Settling,
		FireOnFail:     checks.IsHealthType(typ),
		Now:            deps.Now,
		Emit:           deps.Emit,
		Publish:        publishWatchSnapshots(deps.WatchSnapshots),
		StateStore:     deps.WatchState,
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

// buildMetricWatches expands one multi-metric watch entry (net/icmp/swap) into
// one Watch per metric, each with its own check, window and actions. The per-metric
// check entry is the watch's base check fields plus metric:<key> plus the
// metric block's condition keys (everything except then/for/within). Builder-set
// keys (type, host/interface, count, metric) take precedence over the block.
func buildMetricWatches(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) ([]*Watch, []string) {
	metrics, ok := entry[config.SectionMetrics].(map[string]any)
	if !ok || len(metrics) == 0 {
		return nil, []string{"watch " + name + ": " + cfgval.AsString(checkEntry[checks.CheckKeyType]) + " check requires a non-empty metrics map"}
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
			case rules.RuleFieldThen, rules.RuleFieldFor, rules.RuleFieldWithin:
			default:
				ce[k] = v
			}
		}
		for k, v := range checkEntry { // base check fields win
			ce[k] = v
		}
		ce[checks.CheckKeyMetric] = key

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
			CheckType:      cfgval.AsString(checkEntry[checks.CheckKeyType]),
			Check:          check,
			Window:         rules.ParseWindowRule(mEntry),
			Hook:           actions.hook,
			Notifiers:      resolveNotifiers(actions.effectiveNames, deps.Notifiers),
			NotifyInterval: actions.notifyInterval,
			Emission:       emission.Merge(mEntry[emission.Section], emission.Merge(entry[emission.Section], deps.GlobalEmission)),
			DryRun:         config.DryRun(entry),
			Runner:         OSHookRunner{Runner: deps.ExecxRunner},
			Interval:       interval,
			IsPaused:       monitorPaused(deps.Monitor, watchMonitorKey(name)),
			InPanic:        deps.Panic.Active,
			Settling:       deps.Settling,
			Now:            deps.Now,
			Emit:           deps.Emit,
			Publish:        publishWatchSnapshots(deps.WatchSnapshots),
			StateStore:     deps.WatchState,
			StateSlot:      checks.DataKeyMetric + ":" + key,
		})
	}
	return out, warns
}

// buildFileWatch builds a stateful file watch: a fileWatcher (its own per-path
// baseline, conditions and hook) wired into a Watch through Watch.Cycle so it can
// fire one hook per change. The Watch's check/window fields are unused.
func buildFileWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	if cfgval.AsString(checkEntry[checks.CheckKeyPath]) == "" {
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
		path:      cfgval.AsString(checkEntry[checks.CheckKeyPath]),
		recursive: cfgval.Bool(checkEntry[checks.CheckKeyRecursive]),
		cond:      cond,
		hook:      actions.hook,
		notifiers: resolveNotifiers(actions.effectiveNames, deps.Notifiers),
		dryRun:    config.DryRun(entry),
		inPanic:   deps.Panic.Active,
		runner:    OSHookRunner{Runner: deps.ExecxRunner},
		emit:      deps.Emit,
		publish:   publishWatchSnapshots(deps.WatchSnapshots),
	}
	return &Watch{
		Name:      name,
		CheckType: checks.CheckTypeFile,
		Interval:  interval,
		IsPaused:  monitorPaused(deps.Monitor, watchMonitorKey(name)),
		InPanic:   deps.Panic.Active,
		Settling:  deps.Settling,
		DryRun:    config.DryRun(entry),
		Now:       deps.Now,
		Emit:      deps.Emit,
		Cycle:     fw.runCycle,
	}, ""
}

// buildProcWatch builds a stateful process watch: a procWatcher (its own per-PID
// state, conditions and hook) wired into a Watch through Watch.Cycle so it can
// fire one hook per matching PID.
func buildProcWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	pname := cfgval.AsString(checkEntry[checks.CheckKeyName])
	if pname == "" {
		return nil, "watch " + name + ": process check requires a name"
	}
	cond, err := parseProcCond(checkEntry)
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	match := ProcMatch{Name: pname, User: cfgval.AsString(checkEntry[checks.CheckKeyUser])}
	actions, err := resolveWatchActions(entry, deps, watchActionOptions{
		checkType:    checks.CheckTypeProcess,
		parseKill:    true,
		emptyMessage: "then requires a hook, notify and/or kill",
	})
	if err != nil {
		return nil, "watch " + name + ": " + err.Error()
	}
	if actions.kill != nil {
		selector, err := processWatchKillSelector(match)
		if err != nil {
			return nil, "watch " + name + ": " + err.Error()
		}
		actions.kill.selector = selector
	}
	var resolve process.UserResolver
	if deps.UserLookup != nil {
		resolve = deps.UserLookup.ResolveUser
	}
	pw := &procWatcher{
		name:      name,
		match:     match,
		cond:      cond,
		hook:      actions.hook,
		kill:      actions.kill,
		notifiers: resolveNotifiers(actions.effectiveNames, deps.Notifiers),
		dryRun:    config.DryRun(entry),
		inPanic:   deps.Panic.Active,
		runner:    OSHookRunner{Runner: deps.ExecxRunner},
		resolve:   resolve,
		now:       deps.Now,
		emit:      deps.Emit,
		sampler:   procSamplerFromDeps(deps),
		publish:   publishWatchSnapshots(deps.WatchSnapshots),
	}
	return &Watch{
		Name:      name,
		CheckType: checks.CheckTypeProcess,
		Interval:  interval,
		IsPaused:  monitorPaused(deps.Monitor, watchMonitorKey(name)),
		InPanic:   deps.Panic.Active,
		Settling:  deps.Settling,
		DryRun:    config.DryRun(entry),
		Now:       deps.Now,
		Emit:      deps.Emit,
		Cycle:     pw.runCycle,
	}, ""
}

func processWatchKillSelector(match ProcMatch) (process.KillSelector, error) {
	if match.Name == "" || !filepath.IsAbs(match.Name) {
		return process.KillSelector{}, errors.New("then.kill requires check.name to be an absolute resolved exe path")
	}
	if match.User == "" {
		return process.KillSelector{}, errors.New("then.kill requires check.user")
	}
	return process.KillSelector{Users: []string{match.User}, ExeAny: []string{match.Name}}, nil
}

// parseProcCond reads the for/cpu/memory/io conditions from a process check
// entry. At least one must be present.
func parseProcCond(check map[string]any) (procCond, error) {
	var c procCond
	if _, present := check[checks.CheckKeyFor]; present {
		d := cfgval.Duration(check[checks.CheckKeyFor])
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
		{metrics.MetricCPU, &c.cpuOp, &c.cpuValue},
		{metrics.MetricMemory, &c.memOp, &c.memValue},
		{metrics.MetricIO, &c.ioOp, &c.ioValue},
	} {
		m, ok := check[t.key].(map[string]any)
		if !ok {
			continue
		}
		op := cfgval.AsString(m[checks.CheckKeyOp])
		if !cfgval.IsCompareOp(op) {
			return c, fmt.Errorf("process %s requires a valid op (>=, >, <=, <, ==, !=)", t.key)
		}
		v, ok := cfgval.Float(m[checks.CheckKeyValue])
		if !ok {
			return c, fmt.Errorf("process %s value must be numeric", t.key)
		}
		*t.op, *t.val = op, v
	}
	if v, present := check[checks.CheckKeyGone]; present {
		b, ok := v.(bool)
		if !ok {
			return c, fmt.Errorf("process gone must be a boolean")
		}
		c.onGone = b
	}
	if !c.any() {
		return c, fmt.Errorf("process check requires at least one of %s", config.ProcessWatchConditionSummary)
	}
	return c, nil
}

// parseFileCond reads the size/permissions/owner/existence conditions from a file
// check entry. At least one must be present.
func parseFileCond(check map[string]any) (fileCond, error) {
	var c fileCond
	if sz, ok := check[checks.CheckKeySize].(map[string]any); ok {
		if cfgval.AsString(sz[checks.CheckKeyOn]) == checks.OnModeChange {
			c.sizeChange = true
		} else {
			op := cfgval.AsString(sz[checks.CheckKeyOp])
			if !cfgval.IsCompareOp(op) {
				return c, fmt.Errorf("file size requires on: change or {op, value}")
			}
			v, ok := cfgval.Float(sz[checks.CheckKeyValue])
			if !ok {
				return c, fmt.Errorf("file size value must be numeric")
			}
			c.sizeOp, c.sizeValue = op, v
		}
	}
	if p, ok := check[checks.CheckKeyPermissions].(map[string]any); ok {
		if cfgval.AsString(p[checks.CheckKeyOn]) != checks.OnModeChange {
			return c, fmt.Errorf("file permissions requires on: change")
		}
		c.permChange = true
	}
	if o, ok := check[checks.CheckKeyOwner].(map[string]any); ok {
		if cfgval.AsString(o[checks.CheckKeyOn]) != checks.OnModeChange {
			return c, fmt.Errorf("file owner requires on: change")
		}
		c.ownerChange = true
	}
	if e, ok := check[checks.CheckKeyExistence].(map[string]any); ok {
		if cfgval.AsString(e[checks.CheckKeyOn]) != checks.OnModeDelete {
			return c, fmt.Errorf("file existence requires on: delete")
		}
		c.onDelete = true
	}
	if !c.any() {
		return c, fmt.Errorf("file check requires at least one of %s", config.FileWatchConditionSummary)
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
	raw, present := entry[rules.RuleFieldThen]
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
	if h, ok := then[config.WatchThenKeyHook].(map[string]any); ok {
		cmd := cfgval.StringArray(h[config.WatchHookKeyCommand])
		if len(cmd) == 0 {
			return HookSpec{}, nil, fmt.Errorf("hook requires a non-empty command")
		}
		hook = HookSpec{Command: cmd, Timeout: cfgval.Duration(h[config.WatchHookKeyTimeout])}
		if v, ok := cfgval.IntList(h[config.WatchHookKeyExpectExit]); ok {
			hook.ExpectExit = v
		}
		stdout, warn := checks.ParseOutputMatcher(h[config.WatchHookKeyExpectStdout])
		if warn != "" {
			return HookSpec{}, nil, fmt.Errorf("hook expect_stdout %s", warn)
		}
		stderr, warn := checks.ParseOutputMatcher(h[config.WatchHookKeyExpectStderr])
		if warn != "" {
			return HookSpec{}, nil, fmt.Errorf("hook expect_stderr %s", warn)
		}
		hook.Stdout, hook.Stderr = stdout, stderr
	}
	return hook, cfgval.StringList(then[rules.RuleFieldNotify]), nil
}

type watchActions struct {
	hook           HookSpec
	effectiveNames []string
	expand         *ExpandSpec
	kill           *killSpec
	notifyInterval time.Duration
}

type watchActionOptions struct {
	checkType    string
	parseExpand  bool
	parseKill    bool
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
	var kill *killSpec
	if opts.parseKill {
		kill, err = parseKill(thenBlock)
		if err != nil {
			return watchActions{}, err
		}
	}
	if !hasWatchAction(hook, names, effectiveNames, expand) && kill == nil {
		return watchActions{}, errors.New(opts.emptyMessage)
	}
	return watchActions{
		hook:           hook,
		effectiveNames: effectiveNames,
		expand:         expand,
		kill:           kill,
		notifyInterval: cfgval.Duration(thenBlock[config.WatchThenKeyNotifyInterval]),
	}, nil
}

// parseKill reads a `then.kill` action — a native process-signal action for a
// process watch. It reuses process.ParseKillSignal so the accepted signal set
// (TERM default, or KILL) is the single source of truth shared with config
// validation. escalate turns on the TERM→KILL follow-up with sane default grace
// timeouts.
func parseKill(then map[string]any) (*killSpec, error) {
	raw, present := then[config.WatchThenKeyKill]
	if !present {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("then.kill must be a mapping")
	}
	ks := &killSpec{signal: syscall.SIGTERM}
	if s := cfgval.AsString(m[config.WatchKillKeySignal]); s != "" {
		sig, err := process.ParseKillSignal(s)
		if err != nil {
			return nil, fmt.Errorf("then.kill %w", err)
		}
		ks.signal = sig
	}
	ks.escalate = cfgval.Bool(m[config.WatchKillKeyEscalate])
	ks.termTimeout = cfgval.Duration(m[config.WatchKillKeyTermTimeout])
	ks.killTimeout = cfgval.Duration(m[config.WatchKillKeyKillTimeout])
	if ks.termTimeout <= 0 {
		ks.termTimeout = defaultWatchKillTermTimeout
	}
	if ks.killTimeout <= 0 {
		ks.killTimeout = defaultWatchKillTimeout
	}
	return ks, nil
}

// parseExpand reads a `then.expand` storage-expansion action. It is only valid on
// a storage watch, since the action grows the volume backing the checked path.
func parseExpand(then map[string]any, checkType string) (*ExpandSpec, error) {
	raw, ok := then[config.WatchThenKeyExpand].(map[string]any)
	if !ok {
		return nil, nil
	}
	if !isStorageCheckType(checkType) {
		return nil, fmt.Errorf("then.expand is only valid on a storage watch, not %q", checkType)
	}
	// The same grammar as every *_bytes threshold: an explicit size suffix is
	// required so a raw byte count is never confused with another unit.
	n, ok := cfgval.ByteSize(raw[config.WatchExpandKeyBy])
	if !ok || n == 0 || n > math.MaxInt64 {
		return nil, fmt.Errorf("then.expand requires a positive `by` size with a K/M/G/T suffix (e.g. 5G)")
	}
	return &ExpandSpec{By: int64(n)}, nil
}

func isStorageCheckType(typ string) bool {
	return typ == checks.CheckTypeStorage
}

// resolveNotifiers maps notifier names to the configured notifiers, skipping
// unknown names (config validation reports those; a build-time miss means the
// notifier itself failed to build, already warned by notify.Build).
func resolveNotifiers(names []string, reg map[string]notify.Notifier) []notify.Notifier {
	out := make([]notify.Notifier, 0, len(names))
	hasWall := false
	for _, n := range names {
		if nt, ok := reg[n]; ok && nt.Type() == notify.TypeWall {
			hasWall = true
			break
		}
	}
	for _, n := range names {
		if nt, ok := reg[n]; ok {
			if hasWall && nt.Type() == notify.TypeTTY {
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
		if d := cfgval.Duration(tree[config.EntryKeyInterval]); d > 0 {
			interval = d
		}
		for _, m := range []struct {
			suffix string
			build  func(string, map[string]any, Deps, time.Duration) (*Watch, string)
		}{{config.ServiceMonitorKeyVersion, versionMonitor}, {config.ServiceMonitorKeyConfig, configMonitor}} {
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
	notify, ok := onChangeNotify(tree[config.ServiceMonitorKeyVersion])
	if !ok {
		return nil, ""
	}
	entry := versionCommandEntry(tree)
	if entry == nil {
		return nil, "service " + name + ": version monitor needs commands.version (or preflight.version) in the daemon"
	}
	level, lerr := onChangeVersionLevel(tree[config.ServiceMonitorKeyVersion])
	if lerr != "" {
		return nil, "service " + name + ": version monitor: " + lerr
	}
	entry[checks.CheckKeyType] = checks.CheckTypeCommand
	entry[checks.CheckKeyOnChange] = true
	entry[checks.CheckKeyChangeLevel] = level
	watchName := serviceMonitorWatchName(name, config.ServiceMonitorKeyVersion)
	check, err := checks.BuildInline(watchName, entry, monitorDeps(deps))
	if err != nil {
		return nil, "service " + name + ": version monitor: " + err.Error()
	}
	return monitorWatch(watchName, checks.CheckTypeCommand, check, notify, config.DryRun(tree), deps, interval), ""
}

// configMonitor synthesizes a watch that alerts when the service's config is
// invalid (the daemon's preflight.config test fails) or — with a `path` — when a
// config file changes. nil when the service declares no `config.on_change`.
func configMonitor(name string, tree map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	block, _ := tree[config.ServiceMonitorKeyConfig].(map[string]any)
	notify, ok := onChangeNotify(tree[config.ServiceMonitorKeyConfig])
	if !ok {
		return nil, ""
	}
	entry := map[string]any{checks.CheckKeyType: checks.CheckTypeConfig, checks.CheckKeyOnChange: true}
	if cmdEntry := configTestCommandEntry(tree); cmdEntry != nil {
		if cmd := cmdEntry[checks.CheckKeyCommand]; cmd != nil {
			entry[checks.CheckKeyCommand] = cmd
		}
		if user := cmdEntry[checks.CheckKeyUser]; user != nil {
			entry[checks.CheckKeyUser] = user
		}
	}
	if p, present := block[checks.CheckKeyPath]; present {
		entry[checks.CheckKeyPath] = p
	}
	if entry[checks.CheckKeyCommand] == nil && entry[checks.CheckKeyPath] == nil {
		return nil, "service " + name + ": config monitor needs preflight.config (or a path)"
	}
	watchName := serviceMonitorWatchName(name, config.ServiceMonitorKeyConfig)
	check, err := checks.BuildInline(watchName, entry, monitorDeps(deps))
	if err != nil {
		return nil, "service " + name + ": config monitor: " + err.Error()
	}
	return monitorWatch(watchName, checks.CheckTypeConfig, check, notify, config.DryRun(tree), deps, interval), ""
}

// onChangeNotify reads an `{on_change: {notify: [...]}}` block, returning the
// notifier names and whether on_change is declared.
func onChangeNotify(v any) ([]string, bool) {
	block, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	oc, ok := block[config.ServiceMonitorKeyOnChange].(map[string]any)
	if !ok {
		return nil, false
	}
	return cfgval.StringList(oc[rules.RuleFieldNotify]), true
}

// onChangeVersionLevel reads `version.on_change.level` (major|minor|patch) and
// returns the component count the version monitor compares at, defaulting to
// patch (3 — any version_short change fires) when the level is absent.
func onChangeVersionLevel(v any) (int, string) {
	block, ok := v.(map[string]any)
	if !ok {
		return checks.VersionLevelPatchComponents, ""
	}
	oc, ok := block[config.ServiceMonitorKeyOnChange].(map[string]any)
	if !ok {
		return checks.VersionLevelPatchComponents, ""
	}
	name := cfgval.String(oc[config.ServiceMonitorKeyLevel])
	if name == "" {
		return checks.VersionLevelPatchComponents, ""
	}
	level, ok := checks.VersionLevel(name)
	if !ok {
		return 0, "version.on_change.level " + name + " is not one of " + checks.VersionLevelSummary
	}
	return level, ""
}

// versionCommandEntry returns a copy of the resolved version-command entry
// (preflight.version then commands.version, via the shared resolver), or nil.
func versionCommandEntry(tree map[string]any) map[string]any {
	if entry := checks.VersionCommandEntry(tree, checks.DataKeyVersion); entry != nil {
		return maps.Clone(entry)
	}
	return nil
}

// configTestCommandEntry returns a copy of preflight.config (the daemon's, or
// the service's custom preflight that replaced it), or nil.
func configTestCommandEntry(tree map[string]any) map[string]any {
	if pf, ok := tree[config.SectionPreflight].(map[string]any); ok {
		if entry, ok := pf[config.ServiceMonitorKeyConfig].(map[string]any); ok {
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
	if deps.WatchCheckDeps != nil {
		return *deps.WatchCheckDeps
	}
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
func monitorWatch(name, checkType string, check checks.Check, notify []string, dryRun bool, deps Deps, interval time.Duration) *Watch {
	return &Watch{
		Name:       name,
		CheckType:  checkType,
		Check:      check,
		Notifiers:  resolveNotifiers(effectiveNotify(notify, deps.GlobalNotify), deps.Notifiers),
		Emission:   deps.GlobalEmission,
		DryRun:     dryRun,
		Runner:     OSHookRunner{Runner: deps.ExecxRunner},
		Interval:   interval,
		IsPaused:   monitorPaused(deps.Monitor, watchMonitorKey(name)),
		Settling:   deps.Settling,
		FireOnFail: true, // command/config are health-style: alert (notify) on failure/change
		Now:        deps.Now,
		Emit:       deps.Emit,
		Publish:    publishWatchSnapshots(deps.WatchSnapshots),
		StateStore: deps.WatchState,
	}
}

func publishWatchSnapshots(s *WatchSnapshots) func(string, string, checks.Result) {
	if s == nil {
		return nil
	}
	return s.Publish
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
