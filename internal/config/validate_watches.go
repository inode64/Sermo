package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/conn"
	"sermo/internal/process"
	"sermo/internal/rules"
)

func validateWatches(watches map[string]any, locksDir string, notifiers map[string]struct{}, defaultNotify []string, add func(string, ...any)) {
	for _, name := range slices.Sorted(maps.Keys(watches)) {
		entry, ok := watches[name].(map[string]any)
		if !ok {
			add("watches.%s must be a mapping", name)
			continue
		}
		validateWatchMetadata(name, entry, add)
		if mode, present := entry[keyMonitor]; present {
			validateMonitorMode("watches."+name+".monitor", mode, add)
		}
		if v, ok := entry[keyEnabled].(bool); ok && !v {
			continue
		}

		// Entry-level fields are validated before the check so a watch with a
		// missing/invalid check still reports every other problem in one pass.
		if v, present := entry[keyInterval]; present && !isPositiveDuration(cfgval.String(v)) {
			add("watches.%s.interval %q must be a valid positive duration", name, cfgval.String(v))
		}
		if v, present := entry[keyDryRun]; present {
			if _, ok := v.(bool); !ok {
				add("watches.%s.dry_run must be a boolean", name)
			}
		}
		validateNotifyRefs(name, entry, notifiers, add)
		validateWindow("watches."+name, entry, add)
		validateWatchPolicy("watches."+name, entry, add)

		check, ok := entry["check"].(map[string]any)
		if !ok {
			add("watches.%s.check is required", name)
			continue
		}
		cp := "watches." + name + ".check"
		switch cfgval.String(check["type"]) {
		case checks.CheckTypeStorage:
			// The one single-shot type with its own case: a storage watch may carry
			// a then.expand action, so its hook block allows expand.
			validateStorageFields(cp, check, add)
			validateHookBlock("watches."+name, entry, true, false, defaultNotify, add)
		case checks.CheckTypeNet:
			validateNetCheck(name, check, entry, defaultNotify, add)
		case checks.CheckTypeICMP:
			validateICMPCheck(name, check, entry, defaultNotify, add)
		case checks.CheckTypeSwap:
			validateSwapCheck(name, entry, defaultNotify, add)
		case checks.CheckTypeFile:
			validateFileCheck(name, check, entry, defaultNotify, add)
		case checks.CheckTypeProcess:
			validateProcessWatch(name, check, entry, defaultNotify, add)
		case "":
			add("watches.%s.check.type is required", name)
		default:
			// Any single-shot service check (tcp, http, load, oom, cert, …) can be
			// a host watch: validate its fields with the same per-type validators a
			// checks: section uses and require a hook (section: unified checks).
			if validateWatchableCheck(cp, cfgval.String(check["type"]), check, locksDir, add) {
				validateHookBlock("watches."+name, entry, false, false, defaultNotify, add)
			} else {
				add("watches.%s.check.type %q is not supported", name, cfgval.String(check["type"]))
			}
		}
	}
}

// validateServiceWatches validates a service tree's embedded `watches:` section.
// Fire-and-forget entries run the host-watch runtime with the service's scoped
// check deps, so service-scoped service/metric/process_count checks are permitted
// here while the host-wide process watch and net/icmp/swap multi-metric watches
// are rejected. Entries without then are desugared to checks:, and entries with
// then.action are desugared to checks:+rules:, by expandServiceWatches; this pass
// still validates their grammar when it sees an unexpanded tree. Per-type field
// grammar is shared with host watches.
func validateServiceWatches(tree map[string]any, locksDir string, notifiers map[string]struct{}, defaultNotify []string, add func(string, ...any)) {
	watches, ok := tree["watches"].(map[string]any)
	if !ok {
		if _, present := tree["watches"]; present {
			add("watches must be a mapping of named watches")
		}
		return
	}
	for _, name := range slices.Sorted(maps.Keys(watches)) {
		entry, ok := watches[name].(map[string]any)
		if !ok {
			add("watches.%s must be a mapping", name)
			continue
		}
		if name == "version" || name == "config" {
			add("watches.%s name is reserved for the version/config monitor; rename it", name)
			continue
		}
		if v, ok := entry[keyEnabled].(bool); ok && !v {
			continue
		}
		prefix := "watches." + name
		validateWatchMetadata(name, entry, add)
		if mode, present := entry[keyMonitor]; present {
			validateMonitorMode(prefix+".monitor", mode, add)
		}
		if v, present := entry[keyInterval]; present && !isPositiveDuration(cfgval.String(v)) {
			add("%s.interval %q must be a valid positive duration", prefix, cfgval.String(v))
		}
		if v, present := entry[keyDryRun]; present {
			if _, ok := v.(bool); !ok {
				add("%s.dry_run must be a boolean", prefix)
			}
		}
		if then, ok := entry["then"].(map[string]any); ok {
			if _, present := then["notify"]; present {
				validateNotifySelection(prefix+".then.notify", then["notify"], notifiers, add)
			}
		}
		validateWindow(prefix, entry, add)
		validateWatchPolicy(prefix, entry, add)

		check, ok := entry["check"].(map[string]any)
		if !ok {
			add("%s.check is required", prefix)
			continue
		}
		typ := cfgval.String(check["type"])
		switch {
		case typ == "":
			add("%s.check.type is required", prefix)
			continue
		}
		rawThen, hasThen := entry["then"]
		then, _ := rawThen.(map[string]any)
		if !hasThen {
			if !validateSingleShotCheckFields(prefix+".check", typ, check, locksDir, add) {
				add("%s.check.type %q is not supported", prefix, typ)
			}
			continue
		}
		switch {
		case typ == checks.CheckTypeNet || typ == checks.CheckTypeICMP || typ == checks.CheckTypeSwap:
			add("%s.check.type %q is host-scoped; declare it under the global watches: section", prefix, typ)
			continue
		case typ == checks.CheckTypeProcess:
			add("%s.check.type \"process\" matches host-wide (and can kill); use process_count or metric for service-scoped process monitoring, or a host watch", prefix)
			continue
		case !serviceWatchableType(typ):
			add("%s.check.type %q is not supported", prefix, typ)
			continue
		}
		validateSingleShotCheckFields(prefix+".check", typ, check, locksDir, add)
		if action := cfgval.String(then["action"]); action != "" {
			// A rule-class action (restart/…/block/alert) makes this watch a
			// checks:+rules: desugar target (see expandServiceWatches); validate the
			// action semantics instead of the fire-and-forget hook block.
			validateWatchThenAction(prefix, action, then, add)
		} else {
			// A service watch has no kill action (the process watch is rejected above);
			// a storage watch may still carry a then.expand.
			validateHookBlock(prefix, entry, typ == checks.CheckTypeStorage, false, defaultNotify, add)
		}
	}
}

// isRuleClassAction reports whether a then.action turns a service watch into a
// checks:+rules: desugar target — an operation (restart/start/stop/reload/resume),
// a guard (block) or an alert — rather than a fire-and-forget hook/notify/expand/kill
// watch.
func isRuleClassAction(action string) bool {
	switch rules.ActionType(action) {
	case rules.ActionRestart, rules.ActionStart, rules.ActionStop, rules.ActionReload,
		rules.ActionResume, rules.ActionAlert, rules.ActionBlock:
		return true
	}
	return false
}

// isOperationAction reports whether an action drives the operation engine (the
// actions a guard may block).
func isOperationAction(action string) bool {
	switch rules.ActionType(action) {
	case rules.ActionRestart, rules.ActionStart, rules.ActionStop, rules.ActionReload, rules.ActionResume:
		return true
	}
	return false
}

// validateWatchThenAction validates a unified service watch whose then declares a
// rule-class action. It desugars to a generated check + rule, so its then accepts
// action/message/blocks/notify but not fire-and-forget hook/expand/kill side
// effects or watch-only notification cadence.
func validateWatchThenAction(prefix, action string, then map[string]any, add func(string, ...any)) {
	if !isRuleClassAction(action) {
		add("%s.then.action %q is not one of restart, start, stop, reload, resume, alert, block", prefix, action)
		return
	}
	for _, k := range []string{"hook", "expand", "kill"} {
		if _, has := then[k]; has {
			add("%s.then.%s cannot be combined with an action (a watch is either an operation/alert or a fire-and-forget %s)", prefix, k, k)
		}
	}
	allowed := set("action", "message", "blocks", "notify")
	for _, k := range slices.Sorted(maps.Keys(then)) {
		if _, ok := allowed[k]; !ok {
			add("%s.then.%s is not supported with an action", prefix, k)
		}
	}
	if action == string(rules.ActionBlock) {
		if _, hasNotify := then["notify"]; hasNotify {
			add("%s.then.notify is not supported with action: block; guard rules do not notify", prefix)
		}
		if cfgval.String(then["message"]) == "" {
			add("%s.then.message is required with action: block", prefix)
		}
		blocks := cfgval.StringList(then["blocks"])
		if len(blocks) == 0 {
			add("%s.then requires a non-empty blocks: [list of actions] for a block (guard) action", prefix)
		}
		for _, b := range blocks {
			if !isOperationAction(b) {
				add("%s.then.blocks entry %q must be an operation action (restart/start/stop/reload/resume)", prefix, b)
			}
		}
	} else if action == string(rules.ActionAlert) {
		if cfgval.String(then["message"]) == "" {
			add("%s.then.message is required with action: alert", prefix)
		}
	} else if _, hasBlocks := then["blocks"]; hasBlocks {
		add("%s.then.blocks is only valid with action: block", prefix)
	}
}

// serviceWatchableType reports whether typ can back a service-embedded watch: any
// built-in single-shot type (including the service-scoped service/metric types,
// which have per-service deps here) or a connection protocol. The host-scoped
// multi-metric types (net/icmp/swap) and the host-wide `process` watch are
// rejected by the caller before this is reached.
func serviceWatchableType(typ string) bool {
	if checks.IsSingleShotType(typ) {
		return true
	}
	_, ok := conn.Lookup(typ)
	return ok
}

func validateWatchMetadata(name string, entry map[string]any, add func(string, ...any)) {
	for _, key := range []string{"display_name", "description", "category"} {
		if v, present := entry[key]; present {
			if _, ok := v.(string); !ok {
				add("watches.%s.%s must be a string", name, key)
			}
		}
	}
}

// validateHookBlock validates a `then` action block: a hook and/or a notify list
// (at least one), or a storage-only expand action. The hook command (when present)
// must be a non-empty array with a valid optional timeout. Notifier-name
// references are checked separately by validateNotifyRefs (which has the
// configured notifier set).
func validateHookBlock(prefix string, block map[string]any, allowExpand, allowKill bool, defaultNotify []string, add func(string, ...any)) {
	rawThen, present := block["then"]
	if !present {
		// Absent `then` is valid: the watch is alert/monitor-only. Its `check` +
		// `for` (or per-metric conditions) will still produce "firing" state
		// visible in the web UI (Alerts/Watches tiles, failed filter, state badge)
		// and event log entries, but no hook runs and no notifications are
		// delivered (global defaults are not inherited for bare watches).
		return
	}
	then, ok := rawThen.(map[string]any)
	if !ok {
		add("%s.then must be a mapping", prefix)
		return
	}
	allowed := set("hook", "notify", "notify_interval", "expand", "kill")
	for _, key := range slices.Sorted(maps.Keys(then)) {
		if _, ok := allowed[key]; !ok {
			add("%s.then.%s is not supported", prefix, key)
		}
	}
	hook, hasHook := then["hook"].(map[string]any)
	notify := cfgval.StringList(then["notify"])
	if v, present := then["notify_interval"]; present {
		// notify_interval re-sends the notification as a reminder while the watch
		// stays firing; absent means notify once on the rising edge. It only
		// affects delivery, so it is meaningless without notify targets.
		if !isPositiveDuration(cfgval.String(v)) {
			add("%s.then.notify_interval %q must be a valid positive duration", prefix, cfgval.String(v))
		} else if !HasEffectiveNotifyAction(notify, defaultNotify) {
			add("%s.then.notify_interval has no effect without notify targets", prefix)
		}
	}
	rawExpand, expandPresent := then["expand"]
	expand, hasExpand := rawExpand.(map[string]any)
	switch {
	case expandPresent && !hasExpand:
		add("%s.then.expand must be a mapping with a `by` size", prefix)
	case hasExpand && !allowExpand:
		add("%s.then.expand is only valid on a storage watch", prefix)
	case hasExpand:
		// The same grammar the daemon's parseExpand applies, so a bad size fails
		// `config validate` instead of the next start/reload.
		if by, ok := cfgval.ByteSize(expand["by"]); !ok || by == 0 {
			add("%s.then.expand.by %q must be a positive size with a K/M/G/T suffix (e.g. 5G)", prefix, cfgval.String(expand["by"]))
		}
	}
	rawKill, killPresent := then["kill"]
	kill, hasKill := rawKill.(map[string]any)
	switch {
	case killPresent && !hasKill:
		add("%s.then.kill must be a mapping", prefix)
	case hasKill && !allowKill:
		add("%s.then.kill is only valid on a process watch", prefix)
	case hasKill:
		validateKillAction(prefix, kill, add)
	}
	// An explicit `then: { notify: [none] }` (or with a hook/expand/kill too) is a
	// deliberate monitor-only watch (state in the dashboard and events, no
	// delivery). A present `then` that selects nothing is rejected. Omitting the
	// `then` key entirely is another supported way to get alert-only behavior.
	if !hasHook && !HasEffectiveNotifyAction(notify, defaultNotify) && !hasExpand && !hasKill && !NotifyOptedOut(notify) {
		add("%s.then requires a hook, notify, kill and/or expand", prefix)
		return
	}
	if hasHook {
		if !cfgval.IsNonEmptyStringArray(hook["command"]) {
			add("%s.then.hook.command must be a non-empty array", prefix)
		}
		if v, present := hook["timeout"]; present && !isPositiveDuration(cfgval.String(v)) {
			add("%s.then.hook.timeout %q must be a valid positive duration", prefix, cfgval.String(v))
		}
		validateCommandExpectations(prefix+".then.hook", hook, add)
	}
}

// validateKillAction checks a process watch's `then.kill` action: an optional
// signal (TERM default, or KILL — validated by the same process.ParseKillSignal
// the daemon uses), an optional boolean `escalate`, and the optional grace
// durations that only apply when escalating.
func validateKillAction(prefix string, kill map[string]any, add func(string, ...any)) {
	if s := cfgval.String(kill["signal"]); s != "" {
		if _, err := process.ParseKillSignal(s); err != nil {
			add("%s.then.kill.signal %q must be TERM or KILL", prefix, s)
		}
	}
	if v, present := kill["escalate"]; present {
		if _, ok := v.(bool); !ok {
			add("%s.then.kill.escalate must be a boolean", prefix)
		}
	}
	for _, f := range []string{keyTermTimeout, keyKillTimeout} {
		if v, present := kill[f]; present && !isPositiveDuration(cfgval.String(v)) {
			add("%s.then.kill.%s %q must be a valid positive duration", prefix, f, cfgval.String(v))
		}
	}
}

// validateWatchPolicy checks a watch-level `policy` block — the pacing for a
// firing watch's actions (notably then.expand): an optional positive cooldown
// plus the same max_actions/backoff extras a service policy allows. Unlike a
// service, a watch does not require a cooldown; absent means "fire every cycle".
func validateWatchPolicy(prefix string, entry map[string]any, add addFunc) {
	raw, present := entry[sectionPolicy]
	if !present {
		return
	}
	policy, ok := raw.(map[string]any)
	if !ok {
		add("%s.policy must be a mapping", prefix)
		return
	}
	padd := func(format string, args ...any) { add(prefix+"."+format, args...) }
	if v, has := policy["cooldown"]; has && !isPositiveDuration(cfgval.String(v)) {
		padd("policy.cooldown %q must be a valid positive duration", cfgval.String(v))
	}
	validatePolicyExtras(entry, padd)
}

// HasNotifyAction reports whether names selects at least one real notifier — a
// non-empty selection that is not the `none` sentinel. Shared by config
// validation, the daemon's watch builder and the wizard so the notify-selection
// rule lives in one place.
func HasNotifyAction(names []string) bool {
	return len(names) > 0 && !NotifyOptedOut(names)
}

// NotifyOptedOut reports whether a notify selection is the explicit `none`
// opt-out. Unlike an empty selection (which inherits the global default and is
// inert without one), the sentinel is a deliberate "monitor but never deliver"
// choice — valid anywhere a notify list is, even with no other action.
func NotifyOptedOut(names []string) bool {
	return slices.Contains(names, NotifyNone)
}

// HasEffectiveNotifyAction reports whether a watch ends up delivering to a
// notifier: an explicit selection (HasNotifyAction), or an omitted selection that
// inherits a non-empty defaultNotify. The `none` sentinel always suppresses
// delivery.
func HasEffectiveNotifyAction(names, defaultNotify []string) bool {
	if NotifyOptedOut(names) {
		return false
	}
	return HasNotifyAction(names) || (len(names) == 0 && len(defaultNotify) > 0)
}

// validateMetricWatchEntry rejects entry-level then/for/within on a multi-metric
// watch (net, icmp, swap): those fields belong inside each metric's own block.
func validateMetricWatchEntry(name string, entry map[string]any, add func(string, ...any)) {
	validateInvalidWatchEntryFields(name, "multi-metric", entry, []string{"then", "for", "within"}, "metrics.<name>.%s", add)
}

// validateStatefulWatchEntry rejects entry-level for/within on a file or process
// watch: these use internal per-path/per-PID state and never read the shared
// rules window fields at the entry level.
func validateStatefulWatchEntry(name, typ string, entry map[string]any, add func(string, ...any)) {
	validateInvalidWatchEntryFields(name, typ, entry, []string{"for", "within"}, "", add)
}

func validateInvalidWatchEntryFields(name, typ string, entry map[string]any, keys []string, moveHint string, add func(string, ...any)) {
	for _, key := range keys {
		if _, present := entry[key]; present {
			msg := fmt.Sprintf("watches.%s.%s is not valid on a %s watch", name, key, typ)
			if moveHint != "" {
				msg += fmt.Sprintf("; move it into the metric's own block ("+moveHint+")", key)
			}
			add("%s", msg)
		}
	}
}

// validateNetCheck validates a net interface watch: an interface and a non-empty
// metrics map, each metric with a valid condition and its own hook.
func validateNetCheck(name string, check, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
	validateMetricWatchEntry(name, entry, add)
	if cfgval.String(check["interface"]) == "" {
		add("watches.%s.check.interface is required for a net check", name)
	}
	metrics, ok := entry[sectionMetrics].(map[string]any)
	if !ok || len(metrics) == 0 {
		add("watches.%s.metrics is required and must be non-empty for a net check", name)
		return
	}
	for _, key := range slices.Sorted(maps.Keys(metrics)) {
		prefix := fmt.Sprintf("watches.%s.metrics.%s", name, key)
		m, ok := metrics[key].(map[string]any)
		if !ok {
			add("%s must be a mapping", prefix)
			continue
		}
		validateNetMetricCondition(prefix, key, m, add)
		validateHookBlock(prefix, m, false, false, defaultNotify, add)
		validateWindow(prefix, m, add)
	}
}

// validateNetMetricCondition validates one net metric's condition fields:
// state (expect/on), speed (on: change) or errors (delta + optional counters).
// Shared by the multi-metric net watch and the single-shot net check, so the
// condition grammar cannot drift between the two surfaces.
func validateNetMetricCondition(prefix, metric string, m map[string]any, add addFunc) {
	switch metric {
	case checks.NetMetricState:
		validateStateMetric(prefix, m, add)
	case checks.NetMetricSpeed:
		if cfgval.String(m["on"]) != checks.OnModeChange {
			add("%s requires on: change", prefix)
		}
	case checks.NetMetricErrors:
		delta, ok := m["delta"].(map[string]any)
		if !ok {
			add("%s.delta {op, value} is required", prefix)
		} else {
			validateOpNumeric(prefix+".delta", delta, add)
		}
		if c, present := m["counters"]; present {
			if !cfgval.IsNonEmptyStringArray(c) {
				add("%s.counters must be a non-empty list", prefix)
			}
		}
	case checks.NetMetricAddress:
		exp := cfgval.String(m["expect"])
		onChange := cfgval.String(m["on"]) == checks.OnModeChange
		if exp == "" && !onChange {
			add("%s requires expect: present|absent or on: change", prefix)
		} else if exp != "" && exp != checks.NetAddrPresent && exp != checks.NetAddrAbsent {
			add("%s.expect must be present or absent", prefix)
		}
	default:
		add("%s is not a supported net metric (state, speed, errors, address)", prefix)
	}
}

// validateWatchableCheck validates the fields of a single-shot service check used
// as a host watch and reports whether the type is watchable. service/metric/
// process are excluded: they need per-service context (backend status, a metric
// sampler, process discovery) that the watch builder does not provide.
func validateWatchableCheck(prefix, typ string, fields map[string]any, locksDir string, add addFunc) bool {
	if checks.IsServiceScopedType(typ) {
		return false
	}
	return validateSingleShotCheckFields(prefix, typ, fields, locksDir, add)
}

// validateSwapCheck validates a swap watch: a non-empty metrics map of usage
// (used_pct/free_pct/free_bytes thresholds) and/or io (per-cycle delta), each
// with its own hook (mirrors validateNetCheck).
func validateSwapCheck(name string, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
	validateMetricWatchEntry(name, entry, add)
	metrics, ok := entry[sectionMetrics].(map[string]any)
	if !ok || len(metrics) == 0 {
		add("watches.%s.metrics is required and must be non-empty for a swap check", name)
		return
	}
	for _, key := range slices.Sorted(maps.Keys(metrics)) {
		prefix := fmt.Sprintf("watches.%s.metrics.%s", name, key)
		m, ok := metrics[key].(map[string]any)
		if !ok {
			add("%s must be a mapping", prefix)
			continue
		}
		validateSwapMetricCondition(prefix, key, m, add)
		validateHookBlock(prefix, m, false, false, defaultNotify, add)
		validateWindow(prefix, m, add)
	}
}

// validateSwapMetricCondition validates one swap metric's condition fields:
// usage (level thresholds) or io (per-cycle delta). Shared by the multi-metric
// swap watch and the single-shot swap check, so the condition grammar cannot
// drift between the two surfaces.
func validateSwapMetricCondition(prefix, metric string, m map[string]any, add addFunc) {
	switch metric {
	case checks.SwapMetricUsage:
		validateThresholdPreds(prefix, m, checks.SwapUsageFields, add)
	case checks.SwapMetricIO:
		delta, ok := m["delta"].(map[string]any)
		if !ok {
			add("%s.delta {op, value} is required", prefix)
		} else {
			validateOpNumeric(prefix+".delta", delta, add)
		}
	default:
		add("%s is not a supported swap metric (usage, io)", prefix)
	}
}

// validateStateMetric validates a state metric condition shared by net/icmp:
// expect up|down OR on: change.
func validateStateMetric(prefix string, m map[string]any, add func(string, ...any)) {
	exp := cfgval.String(m["expect"])
	onChange := cfgval.String(m["on"]) == checks.OnModeChange
	if exp == "" && !onChange {
		add("%s requires expect: up|down or on: change", prefix)
	} else if exp != "" && exp != checks.NetStateUp && exp != checks.NetStateDown {
		add("%s.expect must be up or down", prefix)
	}
}

// validateICMPCheck validates an icmp host watch: a host (+ optional positive
// count) and a non-empty metrics map, each metric with a valid condition and its
// own hook.
func validateICMPCheck(name string, check, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
	validateMetricWatchEntry(name, entry, add)
	if cfgval.String(check["host"]) == "" {
		add("watches.%s.check.host is required for an icmp check", name)
	}
	if v, present := check["count"]; present {
		if n, ok := cfgval.Int(v); !ok || n <= 0 {
			add("watches.%s.check.count must be a positive integer", name)
		}
	}
	metrics, ok := entry[sectionMetrics].(map[string]any)
	if !ok || len(metrics) == 0 {
		add("watches.%s.metrics is required and must be non-empty for an icmp check", name)
		return
	}
	for _, key := range slices.Sorted(maps.Keys(metrics)) {
		prefix := fmt.Sprintf("watches.%s.metrics.%s", name, key)
		m, ok := metrics[key].(map[string]any)
		if !ok {
			add("%s must be a mapping", prefix)
			continue
		}
		validateICMPMetricCondition(prefix, key, m, add)
		validateHookBlock(prefix, m, false, false, defaultNotify, add)
		validateWindow(prefix, m, add)
	}
}

// validateICMPMetricCondition validates one icmp metric's condition fields:
// state (expect/on) or latency (threshold xor change). Shared by the
// multi-metric icmp watch and the single-shot icmp check, so the condition
// grammar cannot drift between the two surfaces.
func validateICMPMetricCondition(prefix, metric string, m map[string]any, add addFunc) {
	switch metric {
	case checks.NetMetricState:
		validateStateMetric(prefix, m, add)
	case checks.IcmpMetricLatency:
		th, hasT := m["threshold"].(map[string]any)
		ch, hasC := m["change"].(map[string]any)
		if !hasT && !hasC {
			add("%s requires threshold {op, value} or change {delta}", prefix)
		}
		if hasT && hasC {
			add("%s must set only one of threshold or change", prefix)
		}
		if hasT {
			validateOpNumeric(prefix+".threshold", th, add)
		}
		if hasC {
			if !isNumeric(cfgval.String(ch["delta"])) {
				add("%s.change delta %q must be numeric", prefix, cfgval.String(ch["delta"]))
			}
		}
	default:
		add("%s is not a supported icmp metric (state, latency)", prefix)
	}
}

// validateFileCheck validates a file watch: a path, an optional boolean
// recursive, and at least one attribute condition (size threshold/change,
// permissions/owner on change, existence on delete), plus the entry's hook.
func validateFileCheck(name string, check, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
	validateStatefulWatchEntry(name, "file", entry, add)
	if cfgval.String(check["path"]) == "" {
		add("watches.%s.check.path is required for a file check", name)
	}
	if v, present := check["recursive"]; present {
		if _, ok := v.(bool); !ok {
			add("watches.%s.check.recursive must be a boolean", name)
		}
	}

	conds := 0
	if sz, ok := check["size"].(map[string]any); ok {
		conds++
		if cfgval.String(sz["on"]) != checks.OnModeChange {
			if !isValidCompareOp(cfgval.String(sz["op"])) || !isNumeric(cfgval.String(sz["value"])) {
				add("watches.%s.check.size requires on: change or {op, value} with a numeric value", name)
			}
		}
	}
	for _, attr := range []string{"permissions", "owner"} {
		if m, ok := check[attr].(map[string]any); ok {
			conds++
			if cfgval.String(m["on"]) != checks.OnModeChange {
				add("watches.%s.check.%s requires on: change", name, attr)
			}
		}
	}
	if e, ok := check["existence"].(map[string]any); ok {
		conds++
		if cfgval.String(e["on"]) != "delete" {
			add("watches.%s.check.existence requires on: delete", name)
		}
	}
	if conds == 0 {
		add("watches.%s.check requires at least one of size, permissions, owner, existence", name)
	}

	validateHookBlock("watches."+name, entry, false, false, defaultNotify, add)
}

// validateProcessWatch validates a process watch: a name, an optional user, and
// at least one condition (for duration, or cpu/memory/io {op, value}), plus the
// entry's hook.
func validateProcessWatch(name string, check, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
	validateStatefulWatchEntry(name, "process", entry, add)
	if cfgval.String(check["name"]) == "" {
		add("watches.%s.check.name is required for a process check", name)
	}
	conds := 0
	if v, present := check["for"]; present {
		conds++
		if !isPositiveDuration(cfgval.String(v)) {
			add("watches.%s.check.for %q must be a valid positive duration", name, cfgval.String(v))
		}
	}
	for _, attr := range []string{"cpu", "memory", "io"} {
		m, ok := check[attr].(map[string]any)
		if !ok {
			continue
		}
		conds++
		if !isValidCompareOp(cfgval.String(m["op"])) || !isNumeric(cfgval.String(m["value"])) {
			add("watches.%s.check.%s requires {op, value} with a numeric value", name, attr)
		}
	}
	if v, present := check["gone"]; present {
		if b, ok := v.(bool); !ok {
			add("watches.%s.check.gone must be a boolean", name)
		} else if b {
			conds++
		}
	}
	if conds == 0 {
		add("watches.%s.check requires at least one of for, cpu, memory, io, gone", name)
	}

	// A process watch is the one type that may carry a native `then.kill` action.
	validateHookBlock("watches."+name, entry, false, true, defaultNotify, add)
	validateProcessWatchKillSelector(name, check, entry, add)
}

func validateProcessWatchKillSelector(name string, check, entry map[string]any, add func(string, ...any)) {
	then, ok := entry["then"].(map[string]any)
	if !ok {
		return
	}
	if _, present := then["kill"]; !present {
		return
	}
	if !filepath.IsAbs(cfgval.String(check["name"])) {
		add("watches.%s.then.kill requires check.name to be an absolute resolved exe path", name)
	}
	if cfgval.String(check["user"]) == "" {
		add("watches.%s.then.kill requires check.user", name)
	}
}
