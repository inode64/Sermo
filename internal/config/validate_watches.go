package config

import (
	"fmt"
	"maps"
	"slices"

	"sermo/internal/cfgval"
)

func validateWatches(watches map[string]any, locksDir string, notifiers map[string]struct{}, add func(string, ...any)) {
	for _, name := range slices.Sorted(maps.Keys(watches)) {
		entry, ok := watches[name].(map[string]any)
		if !ok {
			add("watches.%s must be a mapping", name)
			continue
		}
		if v, ok := entry["enabled"].(bool); ok && !v {
			continue
		}

		check, ok := entry["check"].(map[string]any)
		if !ok {
			add("watches.%s.check is required", name)
			continue
		}
		cp := "watches." + name + ".check"
		switch cfgval.String(check["type"]) {
		case "disk":
			validateDiskFields(cp, check, add)
			validateHookBlock("watches."+name, entry, add)
		case "net":
			validateNetCheck(name, check, entry, add)
		case "icmp":
			validateICMPCheck(name, check, entry, add)
		case "swap":
			validateSwapCheck(name, entry, add)
		case "load":
			validateLoadFields(cp, check, add)
			validateHookBlock("watches."+name, entry, add)
		case "oom":
			validateOomFields(cp, check, add)
			validateHookBlock("watches."+name, entry, add)
		case "fds":
			validateThresholdPreds(cp, check, []string{"used_pct", "free", "allocated"}, add)
			validateHookBlock("watches."+name, entry, add)
		case "conntrack":
			validateThresholdPreds(cp, check, []string{"used_pct", "free", "count"}, add)
			validateHookBlock("watches."+name, entry, add)
		case "entropy":
			validateEntropyFields(cp, check, add)
			validateHookBlock("watches."+name, entry, add)
		case "cert":
			validateCertFields(cp, check, add)
			validateHookBlock("watches."+name, entry, add)
		case "zombies":
			validateZombieFields(cp, check, add)
			validateHookBlock("watches."+name, entry, add)
		case "file":
			validateFileCheck(name, check, entry, add)
		case "process":
			validateProcessWatch(name, check, entry, add)
		case "":
			add("watches.%s.check.type is required", name)
		default:
			// Any single-shot service check (tcp, http, command, …) can be a host
			// watch: validate its fields and require a hook (section: unified checks).
			if validateWatchableCheck(cp, cfgval.String(check["type"]), check, locksDir, add) {
				validateHookBlock("watches."+name, entry, add)
			} else {
				add("watches.%s.check.type %q is not supported", name, cfgval.String(check["type"]))
			}
		}

		if v, present := entry["interval"]; present && !isPositiveDuration(cfgval.String(v)) {
			add("watches.%s.interval %q must be a valid positive duration", name, cfgval.String(v))
		}

		validateNotifyRefs(name, entry, notifiers, add)
		validateWindow("watches."+name, entry, add)
	}
}

// validateHookBlock validates a `then` action block: a hook and/or a notify list
// (at least one). The hook command (when present) must be a non-empty array with
// a valid optional timeout. Notifier-name references are checked separately by
// validateNotifyRefs (which has the configured notifier set).
func validateHookBlock(prefix string, block map[string]any, add func(string, ...any)) {
	then, ok := block["then"].(map[string]any)
	if !ok {
		add("%s.then is required", prefix)
		return
	}
	hook, hasHook := then["hook"].(map[string]any)
	notify := cfgval.StringList(then["notify"])
	if !hasHook && len(notify) == 0 {
		add("%s.then requires a hook and/or notify", prefix)
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
	}
}

// validateNetCheck validates a net interface watch: an interface and a non-empty
// metrics map, each metric with a valid condition and its own hook
// (spec 2026-06-06-net-interface-watch §4).
func validateNetCheck(name string, check, entry map[string]any, add func(string, ...any)) {
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
		switch key {
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
		default:
			add("%s is not a supported net metric (state, speed, errors)", prefix)
		}
		validateHookBlock(prefix, m, add)
		validateWindow(prefix, m, add)
	}
}

// validateWatchableCheck validates the fields of a single-shot service check used
// as a host watch and reports whether the type is watchable. service/metric/
// process are excluded: they need per-service context (backend status, a metric
// sampler, process discovery) that the watch builder does not provide.
func validateWatchableCheck(prefix, typ string, fields map[string]any, locksDir string, add addFunc) bool {
	switch typ {
	case "tcp":
		if _, ok := cfgval.Int(fields["port"]); !ok {
			add("%s.port is required and must be numeric for a tcp check", prefix)
		}
	case "ports":
		validatePortsFields(prefix, fields, add)
	case "http":
		validateHTTPFields(prefix, fields, add)
	case "command":
		if !isStringArray(fields["command"]) {
			add("%s.command must be an array, not a shell string", prefix)
		}
	case "binary":
		if cfgval.String(fields["path"]) == "" {
			add("%s.path is required for a binary check", prefix)
		}
	case "libraries":
		if cfgval.String(fields["binary"]) == "" {
			add("%s.binary is required for a libraries check", prefix)
		}
	case "file_exists":
		p := cfgval.String(fields["path"])
		if p == "" {
			add("%s.path is required for a file_exists check", prefix)
		} else if underDir(p, locksDir) {
			add("%s.path must not point under the runtime lock dir %s", prefix, locksDir)
		}
	case "count":
		validateCount(fields, prefix, add)
	default:
		return false
	}
	return true
}

// validateSwapCheck validates a swap watch: a non-empty metrics map of usage
// (used_pct/free_pct/free_bytes thresholds) and/or io (per-cycle delta), each
// with its own hook (mirrors validateNetCheck).
func validateSwapCheck(name string, entry map[string]any, add func(string, ...any)) {
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
		switch key {
		case "usage":
			preds := 0
			for _, field := range []string{"used_pct", "free_pct", "free_bytes"} {
				raw, present := m[field]
				if !present {
					continue
				}
				preds++
				mm, ok := raw.(map[string]any)
				if !ok {
					add("%s.%s must be a mapping {op, value}", prefix, field)
					continue
				}
				validateOpNumeric(prefix+"."+field, mm, add)
			}
			if preds == 0 {
				add("%s requires at least one of used_pct/free_pct/free_bytes", prefix)
			}
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
		validateHookBlock(prefix, m, add)
		validateWindow(prefix, m, add)
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
func validateICMPCheck(name string, check, entry map[string]any, add func(string, ...any)) {
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
		switch key {
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
		validateHookBlock(prefix, m, add)
		validateWindow(prefix, m, add)
	}
}

// validateFileCheck validates a file watch: a path, an optional boolean
// recursive, and at least one attribute condition (size threshold/change,
// permissions/owner on change, existence on delete), plus the entry's hook.
func validateFileCheck(name string, check, entry map[string]any, add func(string, ...any)) {
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

	validateHookBlock("watches."+name, entry, add)
}

// validateProcessWatch validates a process watch: a name, an optional user, and
// at least one condition (for duration, or cpu/memory/io {op, value}), plus the
// entry's hook.
func validateProcessWatch(name string, check, entry map[string]any, add func(string, ...any)) {
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

	validateHookBlock("watches."+name, entry, add)
}
