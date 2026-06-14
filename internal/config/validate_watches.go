package config

import (
	"fmt"
	"maps"
	"slices"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
)

func validateWatches(watches map[string]any, locksDir string, notifiers map[string]struct{}, defaultNotify []string, add func(string, ...any)) {
	for _, name := range slices.Sorted(maps.Keys(watches)) {
		entry, ok := watches[name].(map[string]any)
		if !ok {
			add("watches.%s must be a mapping", name)
			continue
		}
		if mode, present := entry["monitor"]; present {
			validateMonitorMode("watches."+name+".monitor", mode, add)
		}
		if v, ok := entry["enabled"].(bool); ok && !v {
			continue
		}

		// Entry-level fields are validated before the check so a watch with a
		// missing/invalid check still reports every other problem in one pass.
		if v, present := entry["interval"]; present && !isPositiveDuration(cfgval.String(v)) {
			add("watches.%s.interval %q must be a valid positive duration", name, cfgval.String(v))
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
		case "storage", "disk":
			// The one single-shot type with its own case: a storage watch may carry
			// a then.expand action, so its hook block allows expand.
			validateDiskFields(cp, check, add)
			validateHookBlock("watches."+name, entry, true, defaultNotify, add)
		case "net":
			validateNetCheck(name, check, entry, defaultNotify, add)
		case "icmp":
			validateICMPCheck(name, check, entry, defaultNotify, add)
		case "swap":
			validateSwapCheck(name, entry, defaultNotify, add)
		case "file":
			validateFileCheck(name, check, entry, defaultNotify, add)
		case "process":
			validateProcessWatch(name, check, entry, defaultNotify, add)
		case "":
			add("watches.%s.check.type is required", name)
		default:
			// Any single-shot service check (tcp, http, load, oom, cert, …) can be
			// a host watch: validate its fields with the same per-type validators a
			// checks: section uses and require a hook (section: unified checks).
			if validateWatchableCheck(cp, cfgval.String(check["type"]), check, locksDir, add) {
				validateHookBlock("watches."+name, entry, false, defaultNotify, add)
			} else {
				add("watches.%s.check.type %q is not supported", name, cfgval.String(check["type"]))
			}
		}
	}
}

// validateHookBlock validates a `then` action block: a hook and/or a notify list
// (at least one), or a storage-only expand action. The hook command (when present)
// must be a non-empty array with a valid optional timeout. Notifier-name
// references are checked separately by validateNotifyRefs (which has the
// configured notifier set).
func validateHookBlock(prefix string, block map[string]any, allowExpand bool, defaultNotify []string, add func(string, ...any)) {
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
	hook, hasHook := then["hook"].(map[string]any)
	notify := cfgval.StringList(then["notify"])
	if v, present := then["dry_run"]; present {
		if _, ok := v.(bool); !ok {
			add("%s.then.dry_run must be a boolean", prefix)
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
	// An explicit `then: { notify: [none] }` (or with a hook/expand too) is a
	// deliberate monitor-only watch (state in the dashboard and events, no
	// delivery). A present `then` that selects nothing is rejected. Omitting the
	// `then` key entirely is another supported way to get alert-only behavior.
	if !hasHook && !HasEffectiveNotifyAction(notify, defaultNotify) && !hasExpand && !NotifyOptedOut(notify) {
		add("%s.then requires a hook, notify and/or expand", prefix)
		return
	}
	if hasHook {
		list, ok := hook["command"].([]any)
		if !ok || len(list) == 0 {
			add("%s.then.hook.command must be a non-empty array", prefix)
		}
		if v, present := hook["timeout"]; present && !isPositiveDuration(cfgval.String(v)) {
			add("%s.then.hook.timeout %q must be a valid positive duration", prefix, cfgval.String(v))
		}
		if v, present := hook["expect_exit"]; present {
			if _, ok := cfgval.Int(v); !ok {
				add("%s.then.hook.expect_exit must be an integer", prefix)
			}
		}
		validateOutputExpectation(prefix+".then.hook", "expect_stdout", hook["expect_stdout"], add)
		validateOutputExpectation(prefix+".then.hook", "expect_stderr", hook["expect_stderr"], add)
	}
}

// validateWatchPolicy checks a watch-level `policy` block — the pacing for a
// firing watch's actions (notably then.expand): an optional positive cooldown
// plus the same max_actions/backoff extras a service policy allows. Unlike a
// service, a watch does not require a cooldown; absent means "fire every cycle".
func validateWatchPolicy(prefix string, entry map[string]any, add addFunc) {
	raw, present := entry["policy"]
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

// validateMetricWatchEntry flags entry-level then/for/within on a multi-metric
// watch (net, icmp, swap): the runtime reads them only inside each metric's own
// block, so an entry-level copy would be silently ignored.
func validateMetricWatchEntry(name string, entry map[string]any, add func(string, ...any)) {
	for _, key := range []string{"then", "for", "within"} {
		if _, present := entry[key]; present {
			add("watches.%s.%s is ignored on a multi-metric watch; move it into the metric's own block (metrics.<name>.%s)", name, key, key)
		}
	}
}

// validateNetCheck validates a net interface watch: an interface and a non-empty
// metrics map, each metric with a valid condition and its own hook
// (spec 2026-06-06-net-interface-watch §4).
func validateNetCheck(name string, check, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
	validateMetricWatchEntry(name, entry, add)
	if cfgval.String(check["interface"]) == "" {
		add("watches.%s.check.interface is required for a net check", name)
	}
	metrics, ok := entry["metrics"].(map[string]any)
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
		validateHookBlock(prefix, m, false, defaultNotify, add)
		validateWindow(prefix, m, add)
	}
}

// validateNetMetricCondition validates one net metric's condition fields:
// state (expect/on), speed (on: change) or errors (delta + optional counters).
// Shared by the multi-metric net watch and the single-shot net check, so the
// condition grammar cannot drift between the two surfaces.
func validateNetMetricCondition(prefix, metric string, m map[string]any, add addFunc) {
	switch metric {
	case "state":
		validateStateMetric(prefix, m, add)
	case "speed":
		if cfgval.String(m["on"]) != "change" {
			add("%s requires on: change", prefix)
		}
	case "errors":
		delta, ok := m["delta"].(map[string]any)
		if !ok {
			add("%s.delta {op, value} is required", prefix)
		} else {
			validateOpNumeric(prefix+".delta", delta, add)
		}
		if c, present := m["counters"]; present {
			if list, ok := c.([]any); !ok || len(list) == 0 {
				add("%s.counters must be a non-empty list", prefix)
			}
		}
	case "address":
		exp := cfgval.String(m["expect"])
		onChange := cfgval.String(m["on"]) == "change"
		if exp == "" && !onChange {
			add("%s requires expect: present|absent or on: change", prefix)
		} else if exp != "" && exp != "present" && exp != "absent" {
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
	if _, serviceScoped := serviceScopedWatchExclusions[typ]; serviceScoped {
		return false
	}
	return validateSingleShotCheckFields(prefix, typ, fields, locksDir, add)
}

var serviceScopedWatchExclusions = set("service", "metric", "process")

// validateSwapCheck validates a swap watch: a non-empty metrics map of usage
// (used_pct/free_pct/free_bytes thresholds) and/or io (per-cycle delta), each
// with its own hook (mirrors validateNetCheck).
func validateSwapCheck(name string, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
	validateMetricWatchEntry(name, entry, add)
	metrics, ok := entry["metrics"].(map[string]any)
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
		validateHookBlock(prefix, m, false, defaultNotify, add)
		validateWindow(prefix, m, add)
	}
}

// validateSwapMetricCondition validates one swap metric's condition fields:
// usage (level thresholds) or io (per-cycle delta). Shared by the multi-metric
// swap watch and the single-shot swap check, so the condition grammar cannot
// drift between the two surfaces.
func validateSwapMetricCondition(prefix, metric string, m map[string]any, add addFunc) {
	switch metric {
	case "usage":
		validateThresholdPreds(prefix, m, checks.SwapUsageFields, add)
	case "io":
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
	onChange := cfgval.String(m["on"]) == "change"
	if exp == "" && !onChange {
		add("%s requires expect: up|down or on: change", prefix)
	} else if exp != "" && exp != "up" && exp != "down" {
		add("%s.expect must be up or down", prefix)
	}
}

// validateICMPCheck validates an icmp host watch: a host (+ optional positive
// count) and a non-empty metrics map, each metric with a valid condition and its
// own hook (spec 2026-06-06-icmp-host-watch §3).
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
	metrics, ok := entry["metrics"].(map[string]any)
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
		validateHookBlock(prefix, m, false, defaultNotify, add)
		validateWindow(prefix, m, add)
	}
}

// validateICMPMetricCondition validates one icmp metric's condition fields:
// state (expect/on) or latency (threshold xor change). Shared by the
// multi-metric icmp watch and the single-shot icmp check, so the condition
// grammar cannot drift between the two surfaces.
func validateICMPMetricCondition(prefix, metric string, m map[string]any, add addFunc) {
	switch metric {
	case "state":
		validateStateMetric(prefix, m, add)
	case "latency":
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
		if cfgval.String(sz["on"]) != "change" {
			if !isValidDiskOp(cfgval.String(sz["op"])) || !isNumeric(cfgval.String(sz["value"])) {
				add("watches.%s.check.size requires on: change or {op, value} with a numeric value", name)
			}
		}
	}
	for _, attr := range []string{"permissions", "owner"} {
		if m, ok := check[attr].(map[string]any); ok {
			conds++
			if cfgval.String(m["on"]) != "change" {
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

	validateHookBlock("watches."+name, entry, false, defaultNotify, add)
}

// validateProcessWatch validates a process watch: a name, an optional user, and
// at least one condition (for duration, or cpu/memory/io {op, value}), plus the
// entry's hook.
func validateProcessWatch(name string, check, entry map[string]any, defaultNotify []string, add func(string, ...any)) {
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
		if !isValidDiskOp(cfgval.String(m["op"])) || !isNumeric(cfgval.String(m["value"])) {
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

	validateHookBlock("watches."+name, entry, false, defaultNotify, add)
}
