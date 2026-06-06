package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Issue is a single validation finding, scoped to a document or "global".
type Issue struct {
	Scope string
	Msg   string
}

func (i Issue) String() string {
	return fmt.Sprintf("%s: %s", i.Scope, i.Msg)
}

var validBackends = map[string]struct{}{"": {}, "auto": {}, "systemd": {}, "openrc": {}}

// rejectedSecurityToggles are keys under `security:` that try to disable hard
// safety invariants and must never be honored (section 30).
var rejectedSecurityToggles = []string{
	"require_preflight_before_restart",
	"block_restart_on_active_lock",
	"allow_sigkill_by_default",
	"require_kill_selector",
}

// Validate runs the implemented subset of section 30 over a loaded config and
// returns every issue found. An empty slice means the configuration is valid as
// far as the current checks go.
//
// Covered: document kind/name presence and uniqueness, uses/clone resolution and
// cycles, variable existence/nesting/expansion, backend values, engine durations
// and max_parallel_checks, paths.locks rejection, paths.runtime absoluteness,
// security toggles, policy.cooldown/max_actions/backoff, port/expect_status range,
// check/preflight/postflight entry schemas (type, optional, command array form,
// service/process states, metric grammar, file_exists not under the lock dir),
// stop_policy.force_kill/kill_only_if, aliases, and rules (type, if/then, action,
// guard/block constraints, for/within windows, and the condition tree: exactly
// one operator per node, check references, states, and metric scope/catalog with
// system metrics confined to alert rules — directly and indirectly through a
// metric check reference — and the metric threshold form matching the metric's
// available forms).
func Validate(cfg *Config) []Issue {
	var issues []Issue
	issues = append(issues, validateGlobal(cfg)...)
	issues = append(issues, validateDocuments(cfg)...)
	issues = append(issues, validateServices(cfg)...)
	return issues
}

func validateGlobal(cfg *Config) []Issue {
	var issues []Issue
	raw := cfg.Global.Raw
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: "global", Msg: fmt.Sprintf(format, args...)})
	}

	if engine, ok := raw["engine"].(map[string]any); ok {
		if backend := scalarString(engine["backend"]); !isValidBackend(backend) {
			add("engine.backend %q is not one of auto, systemd, openrc", backend)
		}
		for _, field := range []string{"interval", "default_timeout"} {
			if v, present := engine[field]; present && !isPositiveDuration(scalarString(v)) {
				add("engine.%s %q must be a valid positive duration", field, scalarString(v))
			}
		}
		if v, present := engine["max_parallel_checks"]; present {
			if n, ok := scalarInt(v); !ok || n <= 0 {
				add("engine.max_parallel_checks must be an integer > 0")
			}
		}
	}

	if paths, ok := raw["paths"].(map[string]any); ok {
		if _, present := paths["locks"]; present {
			add("paths.locks is not supported in the MVP; runtime locks derive from paths.runtime")
		}
		if runtime := scalarString(paths["runtime"]); runtime != "" && !filepath.IsAbs(runtime) {
			add("paths.runtime %q must be an absolute directory", runtime)
		}
	}

	if security, ok := raw["security"].(map[string]any); ok {
		for _, key := range rejectedSecurityToggles {
			if _, present := security[key]; present {
				add("security.%s is a hard safety invariant and cannot be configured", key)
			}
		}
	}

	cooldown, present := defaultsCooldown(cfg.Global.Defaults)
	switch {
	case !present:
		add("defaults.policy.cooldown is required and must be a positive duration")
	case !isPositiveDuration(cooldown):
		add("defaults.policy.cooldown %q must be a valid positive duration", cooldown)
	}

	return issues
}

func validateDocuments(cfg *Config) []Issue {
	var issues []Issue
	profileCount := map[string]int{}
	serviceCount := map[string]int{}

	for _, doc := range cfg.docs {
		scope := documentScope(doc)
		switch doc.Kind {
		case kindProfile, kindService:
		case "":
			issues = append(issues, Issue{Scope: scope, Msg: "document has no kind (expected profile or service)"})
			continue
		default:
			issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("unknown kind %q (expected profile or service)", doc.Kind)})
			continue
		}
		if doc.Name == "" {
			issues = append(issues, Issue{Scope: scope, Msg: "document has no name"})
			continue
		}
		if doc.Kind == kindProfile {
			profileCount[doc.Name]++
		} else {
			serviceCount[doc.Name]++
		}
	}

	for _, name := range sortedKeys(profileCount) {
		if profileCount[name] > 1 {
			issues = append(issues, Issue{Scope: "profile " + name, Msg: "duplicate profile name"})
		}
	}
	for _, name := range sortedKeys(serviceCount) {
		if serviceCount[name] > 1 {
			issues = append(issues, Issue{Scope: "service " + name, Msg: "duplicate service name"})
		}
	}
	return issues
}

func validateServices(cfg *Config) []Issue {
	var issues []Issue
	for _, name := range cfg.ServiceNames {
		if name == "" {
			continue
		}
		resolved, errs := cfg.Resolve(name)
		for _, e := range errs {
			issues = append(issues, Issue{Scope: name, Msg: e})
		}
		if resolved.Tree == nil {
			continue
		}
		issues = append(issues, validateResolved(name, resolved.Tree, cfg.Global.RuntimeDir())...)
	}
	return issues
}

func validateResolved(name string, tree map[string]any, runtime string) []Issue {
	var issues []Issue
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Scope: name, Msg: fmt.Sprintf(format, args...)})
	}

	if backend := scalarString(tree["backend"]); !isValidBackend(backend) {
		add("backend %q is not one of auto, systemd, openrc", backend)
	}

	cooldown, present := policyCooldown(tree)
	switch {
	case !present:
		add("policy.cooldown is required and must be positive after resolution")
	case !isPositiveDuration(cooldown):
		add("policy.cooldown %q must be a valid positive duration", cooldown)
	}

	walkScalars(tree, func(path, key, value string) {
		switch key {
		case "port":
			if n, err := strconv.Atoi(value); err != nil || n < 1 || n > 65535 {
				add("%s = %q must resolve to a port in 1..65535", path, value)
			}
		case "expect_status":
			if n, err := strconv.Atoi(value); err != nil || n < 100 || n > 599 {
				add("%s = %q must resolve to a valid HTTP status", path, value)
			}
		}
	})

	locksDir := filepath.Join(runtime, "locks")
	validateCheckSection(tree, "checks", locksDir, add)
	validateCheckSection(tree, "preflight", locksDir, add)
	validateCheckSection(tree, "postflight", locksDir, add)
	validateStopPolicy(tree, add)
	validatePolicyExtras(tree, add)
	validateAliases(tree, add)
	validateRules(tree, add)

	return issues
}

// ---- section 30: checks, stop_policy, policy, aliases, rules ----

var knownCheckTypes = set("tcp", "http", "command", "service", "file_exists", "binary", "process", "metric", "libraries")
var serviceStates = set("active", "inactive", "failed", "unknown")
var processStates = set("running", "zombie", "absent")
var validActions = set("restart", "start", "stop", "alert", "block")
var metricOps = set(">", ">=", "<", "<=", "==", "!=")
var metricCatalog = map[string]map[string]struct{}{
	"service": set("memory", "cpu", "process_count"),
	"system":  set("total_memory", "total_cpu", "load1", "load5", "load15"),
}

// metricForms records which value forms each metric exposes (section 12/14), so
// a threshold's form can be checked against the metric.
type metricForm struct{ absolute, percent bool }

var metricForms = map[string]metricForm{
	"memory":        {absolute: true, percent: true},
	"cpu":           {percent: true},
	"process_count": {absolute: true},
	"total_memory":  {absolute: true, percent: true},
	"total_cpu":     {percent: true},
	"load1":         {absolute: true},
	"load5":         {absolute: true},
	"load15":        {absolute: true},
}

type addFunc func(format string, args ...any)

// validateCheckSection validates a checks/preflight/postflight section: known
// types, optional booleans, command array form, valid service/process states,
// metric grammar, and that file_exists never points at Sermo's own lock dir.
func validateCheckSection(tree map[string]any, section, locksDir string, add addFunc) {
	entries, ok := tree[section].(map[string]any)
	if !ok {
		return
	}
	for _, name := range sortedKeys(entries) {
		path := section + "." + name
		entry, ok := entries[name].(map[string]any)
		if !ok {
			add("%s must be a mapping", path)
			continue
		}
		if v, present := entry["optional"]; present {
			if _, isBool := v.(bool); !isBool {
				add("%s.optional must be a boolean", path)
			}
		}
		typ := scalarString(entry["type"])
		if typ == "" {
			add("%s has no type", path)
			continue
		}
		if _, known := knownCheckTypes[typ]; !known {
			add("%s has unknown type %q", path, typ)
			continue
		}
		switch typ {
		case "command":
			if !isStringArray(entry["command"]) {
				add("%s command must be an array, not a shell string", path)
			}
		case "service":
			if st := scalarString(entry["expect"]); st != "" {
				if _, ok := serviceStates[st]; !ok {
					add("%s expect %q is not one of active, inactive, failed, unknown", path, st)
				}
			}
		case "process":
			if st := scalarString(entry["state"]); st != "" {
				if _, ok := processStates[st]; !ok {
					add("%s state %q is not one of running, zombie, absent", path, st)
				}
			}
		case "file_exists":
			if p := scalarString(entry["path"]); p != "" && underDir(p, locksDir) {
				add("%s file_exists must not point under the runtime lock dir %s", path, locksDir)
			}
		case "metric":
			validateMetric(entry, path, true, add)
		}
	}
}

func validateStopPolicy(tree map[string]any, add addFunc) {
	sp, ok := tree["stop_policy"].(map[string]any)
	if !ok {
		return
	}
	force, _ := sp["force_kill"].(bool)
	koi, hasKoi := sp["kill_only_if"].(map[string]any)
	if force && !hasKoi {
		add("stop_policy.force_kill=true requires kill_only_if")
	}
	if hasKoi {
		if len(stringSlice(koi["users"])) == 0 || len(stringSlice(koi["exe_any"])) == 0 {
			add("stop_policy.kill_only_if must define both users and exe_any, each non-empty")
		}
	}
}

func validatePolicyExtras(tree map[string]any, add addFunc) {
	policy, ok := tree["policy"].(map[string]any)
	if !ok {
		return
	}
	if v, present := policy["max_actions"]; present {
		if n, ok := scalarInt(v); !ok || n <= 0 {
			add("policy.max_actions must be an integer > 0")
		}
		if _, hasWindow := policy["max_actions_window"]; !hasWindow {
			add("policy.max_actions requires policy.max_actions_window")
		}
	}
	if v, present := policy["max_actions_window"]; present && !isPositiveDuration(scalarString(v)) {
		add("policy.max_actions_window %q must be a valid positive duration", scalarString(v))
	}
	if bo, ok := policy["backoff"].(map[string]any); ok {
		initial := scalarString(bo["initial"])
		if !isPositiveDuration(initial) {
			add("policy.backoff.initial must be a valid positive duration")
		}
		di, _ := time.ParseDuration(initial)
		dm, errMax := time.ParseDuration(scalarString(bo["max"]))
		if errMax != nil || dm < di {
			add("policy.backoff.max must be >= initial")
		}
	}
}

func validateAliases(tree map[string]any, add addFunc) {
	aliases, ok := tree["aliases"].(map[string]any)
	if !ok {
		return
	}
	for _, k := range sortedKeys(aliases) {
		if k != "systemd" && k != "openrc" {
			add("aliases key %q is not a valid backend (systemd, openrc)", k)
			continue
		}
		if len(stringSlice(aliases[k])) == 0 {
			add("aliases.%s must be a non-empty list", k)
		}
	}
}

func validateRules(tree map[string]any, add addFunc) {
	ruleMap, ok := tree["rules"].(map[string]any)
	if !ok {
		return
	}
	checkNames := collectCheckNames(tree)
	systemMetricChecks := collectSystemMetricChecks(tree)

	for _, name := range sortedKeys(ruleMap) {
		path := "rules." + name
		entry, ok := ruleMap[name].(map[string]any)
		if !ok {
			add("%s must be a mapping", path)
			continue
		}

		rtype := scalarString(entry["type"])
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
		action := scalarString(then["action"])
		if action != "" {
			if _, ok := validActions[action]; !ok {
				add("%s then.action %q is not one of restart, start, stop, alert, block", path, action)
			}
		}

		isGuard := rtype == "guard"
		blocks := stringSlice(entry["blocks"])
		if isGuard {
			if len(blocks) == 0 {
				add("%s guard requires a non-empty blocks list", path)
			}
			if action != "block" {
				add("%s guard rules must use action block", path)
			}
		} else {
			if len(blocks) > 0 {
				add("%s only guard rules may set blocks", path)
			}
			if action == "block" {
				add("%s only guard rules may use action block", path)
			}
		}
		if (action == "block" || action == "alert") && scalarString(then["message"]) == "" {
			add("%s action %s requires a non-empty message", path, action)
		}

		_, hasFor := entry["for"]
		_, hasWithin := entry["within"]
		if hasFor && hasWithin {
			add("%s cannot define both for and within", path)
		}
		if f, ok := entry["for"].(map[string]any); ok {
			if c, _ := scalarInt(f["cycles"]); c <= 0 {
				add("%s for.cycles must be > 0", path)
			}
		}
		if wn, ok := entry["within"].(map[string]any); ok {
			cycles, _ := scalarInt(wn["cycles"])
			matches, _ := scalarInt(wn["min_matches"])
			if cycles <= 0 {
				add("%s within.cycles must be > 0", path)
			}
			if matches <= 0 {
				add("%s within.min_matches must be > 0", path)
			}
			if cycles > 0 && matches > cycles {
				add("%s within.min_matches must be <= within.cycles", path)
			}
		}

		if hasIf {
			validateCondition(ifNode, path+".if", checkNames, systemMetricChecks, rtype == "alert", add)
		}
	}
}

var conditionOperators = []string{"and", "or", "not", "failed", "active", "metric", "service", "process", "file", "command"}

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
		if m, ok := node["file"].(map[string]any); !ok || scalarString(m["path"]) == "" {
			add("%s.file requires a path", path)
		}
	case "command":
		m, _ := node["command"].(map[string]any)
		if !isStringArray(m["command"]) {
			add("%s.command must use array form, not a shell string", path)
		}
		if scalarString(m["timeout"]) == "" {
			add("%s.command condition must declare a timeout", path)
		}
	case "metric":
		if m, ok := node["metric"].(map[string]any); ok {
			validateMetric(m, path+".metric", allowSystemMetric, add)
		}
	}
}

func validateProbe(v any, path string, checkNames, systemMetricChecks map[string]struct{}, allowSystemMetric bool, add addFunc) {
	m, ok := v.(map[string]any)
	if !ok {
		add("%s must be a mapping", path)
		return
	}
	if ref := scalarString(m["check"]); ref != "" {
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
	st := scalarString(m[field])
	if st == "" {
		return // defaulted
	}
	if _, ok := valid[st]; !ok {
		add("%s.%s %q is not one of %s", path, field, st, list)
	}
}

func validateMetric(entry map[string]any, path string, allowSystem bool, add addFunc) {
	scope := scalarString(entry["scope"])
	if scope == "" {
		scope = "service"
	}
	catalog, ok := metricCatalog[scope]
	if !ok {
		add("%s scope %q is not service or system", path, scope)
		return
	}
	name := scalarString(entry["name"])
	known := false
	if name == "" {
		add("%s requires a metric name", path)
	} else if _, ok := catalog[name]; !ok {
		add("%s metric %q is not in the %s catalog", path, name, scope)
	} else {
		known = true
	}
	if op := scalarString(entry["op"]); op != "" {
		if _, ok := metricOps[op]; !ok {
			add("%s op %q is not one of >, >=, <, <=, ==, !=", path, op)
		}
	}
	value := scalarString(entry["value"])
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
			if e, ok := raw.(map[string]any); ok && scalarString(e["type"]) == "metric" && scalarString(e["scope"]) == "system" {
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

func underDir(path, dir string) bool {
	clean := filepath.Clean(path)
	dir = filepath.Clean(dir)
	return clean == dir || strings.HasPrefix(clean, dir+string(filepath.Separator))
}

func isStringArray(v any) bool {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return false
	}
	for _, e := range list {
		if _, ok := e.(string); !ok {
			return false
		}
	}
	return true
}

func stringSlice(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s := scalarString(e); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func scalarInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case uint64:
		return int(t), true
	case float64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		return n, err == nil
	default:
		return 0, false
	}
}

func set(values ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, v := range values {
		out[v] = struct{}{}
	}
	return out
}

func defaultsCooldown(defaults map[string]any) (string, bool) {
	policy, ok := defaults["policy"].(map[string]any)
	if !ok {
		return "", false
	}
	v, present := policy["cooldown"]
	if !present {
		return "", false
	}
	return scalarString(v), true
}

func policyCooldown(tree map[string]any) (string, bool) {
	policy, ok := tree["policy"].(map[string]any)
	if !ok {
		return "", false
	}
	v, present := policy["cooldown"]
	if !present {
		return "", false
	}
	return scalarString(v), true
}

// walkScalars visits every scalar leaf in the tree (skipping the `variables`
// section, whose raw values are not target-typed fields), reporting the dotted
// path, the leaf key and its stringified value.
func walkScalars(tree map[string]any, visit func(path, key, value string)) {
	for k, v := range tree {
		if k == "variables" {
			continue
		}
		walkScalarValue(k, k, v, visit)
	}
}

func walkScalarValue(path, key string, v any, visit func(path, key, value string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			walkScalarValue(path+"."+k, k, e, visit)
		}
	case []any:
		for i, e := range t {
			walkScalarValue(fmt.Sprintf("%s[%d]", path, i), key, e, visit)
		}
	default:
		visit(path, key, scalarString(t))
	}
}

func isValidBackend(b string) bool {
	_, ok := validBackends[b]
	return ok
}

func isPositiveDuration(s string) bool {
	d, err := time.ParseDuration(s)
	return err == nil && d > 0
}

func documentScope(doc *Document) string {
	kind := doc.Kind
	if kind == "" {
		kind = "document"
	}
	if doc.Name != "" {
		return kind + " " + doc.Name
	}
	return fmt.Sprintf("%s %s", kind, filepath.Base(doc.Path))
}
