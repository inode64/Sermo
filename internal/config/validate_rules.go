package config

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"sermo/internal/cfgval"
)

// validateWindow checks an optional for/within firing window at the dotted prefix,
// shared by rules, host watches and per-metric sub-watches. A window may declare
// at most one of for/within; for.cycles and within.cycles must be positive; and
// within.min_matches — optional, defaulting to 1 (true at least once within the
// window) — must be positive and no larger than within.cycles when declared.
func validateWindow(prefix string, entry map[string]any, add addFunc) {
	rawFor, hasFor := entry["for"]
	rawWithin, hasWithin := entry["within"]
	if hasFor && hasWithin {
		add("%s cannot define both for and within", prefix)
	}
	if hasFor {
		f, ok := rawFor.(map[string]any)
		if !ok {
			// A scalar (`for: 3`) would otherwise be silently ignored by the
			// runtime parser, leaving the rule without a window.
			add("%s.for must be a mapping, e.g. for: {cycles: 3}", prefix)
		} else if c, _ := cfgval.Int(f["cycles"]); c <= 0 {
			add("%s.for.cycles must be > 0", prefix)
		}
	}
	if hasWithin {
		wn, ok := rawWithin.(map[string]any)
		if !ok {
			add("%s.within must be a mapping, e.g. within: {cycles: 5, min_matches: 2}", prefix)
			return
		}
		cycles, _ := cfgval.Int(wn["cycles"])
		if cycles <= 0 {
			add("%s.within.cycles must be > 0", prefix)
		}
		if v, present := wn["min_matches"]; present {
			matches, _ := cfgval.Int(v)
			switch {
			case matches <= 0:
				add("%s.within.min_matches must be > 0", prefix)
			case cycles > 0 && matches > cycles:
				add("%s.within.min_matches must be <= within.cycles", prefix)
			}
		}
	}
}

var serviceStates = set("active", "inactive", "failed", "unknown")
var processStates = set("running", "zombie", "absent")
var validActions = set("restart", "start", "stop", "reload", "alert", "block")
var metricCatalog = map[string]map[string]struct{}{
	"service": set("memory", "swap", "cpu", "cpu_thread", "process_count", "io", "io_read", "io_write", "fds", "threads"),
	"system":  set("total_memory", "total_cpu", "load1", "load5", "load15"),
}

// metricForms records which value forms each metric exposes (section 12/14), so
// a threshold's form can be checked against the metric.
type metricForm struct{ absolute, percent bool }

var metricForms = map[string]metricForm{
	"memory":        {absolute: true, percent: true},
	"swap":          {absolute: true, percent: true},
	"cpu":           {percent: true},
	"cpu_thread":    {percent: true},
	"process_count": {absolute: true},
	"io":            {absolute: true},
	"io_read":       {absolute: true},
	"io_write":      {absolute: true},
	"fds":           {absolute: true},
	"threads":       {absolute: true},
	"total_memory":  {absolute: true, percent: true},
	"total_cpu":     {percent: true},
	"load1":         {absolute: true},
	"load5":         {absolute: true},
	"load15":        {absolute: true},
}

// validateRuleWindow checks the merged `rule_window` fallback block (section 13):
// a positive cycles count, a known mode, and — for the within mode — an optional
// min_matches (default 1) that is positive and no larger than cycles when
// declared.
func validateRuleWindow(tree map[string]any, add addFunc) {
	rw, present := tree["rule_window"]
	if !present {
		return
	}
	m, ok := rw.(map[string]any)
	if !ok {
		add("rule_window must be a mapping")
		return
	}
	cycles, _ := cfgval.Int(m["cycles"])
	if cycles <= 0 {
		add("rule_window.cycles must be > 0")
	}
	switch mode := cfgval.String(m["mode"]); mode {
	case "", "consecutive":
	case "within":
		if v, present := m["min_matches"]; present {
			matches, _ := cfgval.Int(v)
			switch {
			case matches <= 0:
				add("rule_window.min_matches must be > 0 for mode %q", mode)
			case cycles > 0 && matches > cycles:
				add("rule_window.min_matches must be <= rule_window.cycles")
			}
		}
	default:
		add("rule_window.mode %q is not one of consecutive, within", mode)
	}
}

func validateRules(tree map[string]any, notifiers map[string]struct{}, add addFunc) {
	ruleMap, ok := tree["rules"].(map[string]any)
	if !ok {
		return
	}
	checkNames := collectCheckNames(tree)
	systemMetricChecks := collectSystemMetricChecks(tree)

	for _, name := range slices.Sorted(maps.Keys(ruleMap)) {
		path := "rules." + name
		entry, ok := ruleMap[name].(map[string]any)
		if !ok {
			add("%s must be a mapping", path)
			continue
		}

		if _, present := entry["notify"]; present {
			validateNotifySelection(path+".notify", cfgval.StringList(entry["notify"]), notifiers, add)
		}

		rtype := cfgval.String(entry["type"])
		switch rtype {
		case "remediation", "guard", "alert":
		default:
			add("%s type %q is not one of remediation, guard, alert", path, rtype)
		}

		ifNode, hasIf := entry["if"].(map[string]any)
		if !hasIf {
			add("%s has no if condition", path)
		}
		then, hasThen := entry["then"].(map[string]any)
		if !hasThen {
			add("%s has no then action", path)
		}
		actions := ruleActions(then)
		isGuard := rtype == "guard"
		blocks := cfgval.StringList(entry["blocks"])
		hasBlock := false
		for _, act := range actions {
			if act.typ != "" {
				if _, ok := validActions[act.typ]; !ok {
					add("%s then.action %q is not one of restart, start, stop, reload, alert, block", path, act.typ)
				}
			}
			if act.typ == "block" {
				hasBlock = true
				if !isGuard {
					add("%s only guard rules may use action block", path)
				}
			}
			if (act.typ == "block" || act.typ == "alert") && act.message == "" {
				add("%s action %s requires a non-empty message", path, act.typ)
			}
		}
		if isGuard {
			if len(blocks) == 0 {
				add("%s guard requires a non-empty blocks list", path)
			}
			if !hasBlock {
				add("%s guard rules must use action block", path)
			}
		} else if len(blocks) > 0 {
			add("%s only guard rules may set blocks", path)
		}

		// Operation actions belong to remediation rules: an alert/guard rule
		// carrying one would validate and then silently never run it, and a
		// remediation rule without one is an alert in disguise.
		hasOperation := false
		for _, act := range actions {
			switch act.typ {
			case "restart", "start", "stop", "reload":
				hasOperation = true
				if rtype != "remediation" {
					add("%s only remediation rules may use action %s", path, act.typ)
				}
			}
		}
		if rtype == "remediation" && hasThen && !hasOperation {
			add("%s remediation requires an operation action (restart, start, stop, reload); use type: alert for notify-only rules", path)
		}

		validateWindow(path, entry, add)

		if hasIf {
			validateCondition(ifNode, path+".if", checkNames, systemMetricChecks, rtype == "alert", add)
		}
	}
}

var conditionOperators = []string{"and", "or", "not", "failed", "active", "metric", "service", "process", "file", "command", "changed"}

// validateCondition checks one condition node: exactly one operator/leaf, valid
// check references, valid service/process states, command array+timeout, and
// metric grammar (with system-scope allowed only in alert rules).
func validateCondition(node map[string]any, path string, checkNames, systemMetricChecks map[string]struct{}, allowSystemMetric bool, add addFunc) {
	present := presentOperators(node)
	if len(present) != 1 {
		add("%s must contain exactly one condition/operator", path)
		return
	}
	key := present[0]

	switch key {
	case "and", "or":
		items, ok := node[key].([]any)
		if !ok || len(items) == 0 {
			add("%s.%s requires a non-empty list", path, key)
			return
		}
		for i, item := range items {
			child, ok := item.(map[string]any)
			if !ok {
				add("%s.%s[%d] must be a condition", path, key, i)
				continue
			}
			validateCondition(child, fmt.Sprintf("%s.%s[%d]", path, key, i), checkNames, systemMetricChecks, allowSystemMetric, add)
		}
	case "not":
		child, ok := node["not"].(map[string]any)
		if !ok {
			add("%s.not must be a condition", path)
			return
		}
		validateCondition(child, path+".not", checkNames, systemMetricChecks, allowSystemMetric, add)
	case "failed", "active":
		validateProbe(node[key], path+"."+key, checkNames, systemMetricChecks, allowSystemMetric, add)
	case "service":
		validateState(node["service"], "state", serviceStates, "active, inactive, failed, unknown", path+".service", add)
	case "process":
		validateState(node["process"], "state", processStates, "running, zombie, absent", path+".process", add)
	case "file":
		m, ok := node["file"].(map[string]any)
		if !ok || cfgval.String(m["path"]) == "" {
			add("%s.file requires a path", path)
		}
		if ok {
			// `exists` defaults to true at runtime; a non-boolean (e.g. the
			// string "false") would silently act as true.
			if v, present := m["exists"]; present {
				if _, isBool := v.(bool); !isBool {
					add("%s.file.exists must be a boolean", path)
				}
			}
		}
	case "command":
		m, _ := node["command"].(map[string]any)
		if !isStringArray(m["command"]) {
			add("%s.command must use array form, not a shell string", path)
		}
		if cfgval.String(m["timeout"]) == "" {
			add("%s.command condition must declare a timeout", path)
		}
	case "metric":
		if m, ok := node["metric"].(map[string]any); ok {
			validateMetric(m, path+".metric", allowSystemMetric, add)
		}
	case "changed":
		if m, ok := node["changed"].(map[string]any); !ok || cfgval.String(m["path"]) == "" {
			add("%s.changed requires a path", path)
		}
	}
}

func validateProbe(v any, path string, checkNames, systemMetricChecks map[string]struct{}, allowSystemMetric bool, add addFunc) {
	m, ok := v.(map[string]any)
	if !ok {
		add("%s must be a mapping", path)
		return
	}
	if ref := cfgval.String(m["check"]); ref != "" {
		if _, ok := checkNames[ref]; !ok {
			add("%s references unknown check %q", path, ref)
		} else if _, isSys := systemMetricChecks[ref]; isSys && !allowSystemMetric {
			add("%s references system metric check %q, which is only allowed in alert rules", path, ref)
		}
		return
	}
	if len(m) != 1 {
		add("%s inline probe must have exactly one type key", path)
		return
	}
	for k := range m {
		if _, ok := knownCheckTypes[k]; !ok {
			add("%s inline probe type %q is unknown", path, k)
		}
	}
}

func validateState(v any, field string, valid map[string]struct{}, list, path string, add addFunc) {
	m, ok := v.(map[string]any)
	if !ok {
		add("%s must be a mapping", path)
		return
	}
	st := cfgval.String(m[field])
	if st == "" {
		return // defaulted
	}
	if _, ok := valid[st]; !ok {
		add("%s.%s %q is not one of %s", path, field, st, list)
	}
}

func validateMetric(entry map[string]any, path string, allowSystem bool, add addFunc) {
	scope := cfgval.String(entry["scope"])
	if scope == "" {
		scope = "service"
	}
	catalog, ok := metricCatalog[scope]
	if !ok {
		add("%s scope %q is not service or system", path, scope)
		return
	}
	name := cfgval.String(entry["name"])
	known := false
	if name == "" {
		add("%s requires a metric name", path)
	} else if _, ok := catalog[name]; !ok {
		add("%s metric %q is not in the %s catalog", path, name, scope)
	} else {
		known = true
	}
	if op := cfgval.String(entry["op"]); op != "" {
		if !cfgval.IsCompareOp(op) {
			add("%s op %q is not one of >, >=, <, <=, ==, !=", path, op)
		}
	}
	value := cfgval.String(entry["value"])
	if !parseMetricValue(value) {
		add("%s value %q must be a number with an optional trailing %%", path, value)
	} else if known {
		// Form must match: a "%" threshold needs a percentage form, a bare number
		// an absolute form (section 14).
		form := metricForms[name]
		if strings.HasSuffix(strings.TrimSpace(value), "%") {
			if !form.percent {
				add("%s uses a %% threshold but metric %q has no percentage form", path, name)
			}
		} else if !form.absolute {
			add("%s uses an absolute threshold but metric %q has no absolute form", path, name)
		}
	}
	if scope == "system" && !allowSystem {
		add("%s scope: system metric is only allowed in alert rules", path)
	}
}

type valAction struct {
	typ     string
	message string
}

// ruleActions returns a rule's actions, supporting both the single
// `then: {action, message}` and the multi `then: {actions: [...]}` forms.
func ruleActions(then map[string]any) []valAction {
	if list, ok := then["actions"].([]any); ok {
		out := make([]valAction, 0, len(list))
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				out = append(out, valAction{typ: cfgval.String(m["type"]), message: cfgval.String(m["message"])})
			}
		}
		return out
	}
	return []valAction{{typ: cfgval.String(then["action"]), message: cfgval.String(then["message"])}}
}

func collectCheckNames(tree map[string]any) map[string]struct{} {
	names := map[string]struct{}{}
	for _, section := range []string{"checks", "preflight"} {
		if entries, ok := tree[section].(map[string]any); ok {
			for name := range entries {
				names[name] = struct{}{}
			}
		}
	}
	return names
}

// collectSystemMetricChecks returns the names of checks that are scope:system
// metrics, so a remediation rule referencing one (via failed/active) can be
// flagged (section 30).
func collectSystemMetricChecks(tree map[string]any) map[string]struct{} {
	names := map[string]struct{}{}
	for _, section := range []string{"checks", "preflight"} {
		entries, ok := tree[section].(map[string]any)
		if !ok {
			continue
		}
		for name, raw := range entries {
			if e, ok := raw.(map[string]any); ok && cfgval.String(e["type"]) == "metric" && cfgval.String(e["scope"]) == "system" {
				names[name] = struct{}{}
			}
		}
	}
	return names
}

func presentOperators(node map[string]any) []string {
	var present []string
	for _, op := range conditionOperators {
		if _, ok := node[op]; ok {
			present = append(present, op)
		}
	}
	return present
}

func parseMetricValue(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.HasSuffix(s, "%") {
		n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "%")), 64)
		return err == nil && n >= 0 && n <= 100
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}
