package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"

	"sermo/internal/conn"
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
// (including operation_timeout, the non-negative startup_delay) and
// max_parallel_checks/max_parallel_operations, paths.locks
// rejection, paths.runtime absoluteness,
// security toggles, policy.cooldown/max_actions/backoff, port/expect_status range,
// check/preflight/postflight entry schemas (type, optional, command array form,
// service/process states, metric grammar, file_exists not under the lock dir),
// stop_policy.force_kill/kill_only_if, service, and rules (type, if/then, action,
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
		for _, field := range []string{"interval", "default_timeout", "operation_timeout"} {
			if v, present := engine[field]; present && !isPositiveDuration(scalarString(v)) {
				add("engine.%s %q must be a valid positive duration", field, scalarString(v))
			}
		}
		if v, present := engine["startup_delay"]; present && !isNonNegativeDuration(scalarString(v)) {
			add("engine.startup_delay %q must be a valid non-negative duration (0 disables the wait)", scalarString(v))
		}
		if v, present := engine["max_parallel_checks"]; present {
			if n, ok := scalarInt(v); !ok || n <= 0 {
				add("engine.max_parallel_checks must be an integer > 0")
			}
		}
		if v, present := engine["max_parallel_operations"]; present {
			if n, ok := scalarInt(v); !ok || n <= 0 {
				add("engine.max_parallel_operations must be an integer > 0")
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
		if stateDir := scalarString(paths["state"]); stateDir != "" && !filepath.IsAbs(stateDir) {
			add("paths.state %q must be an absolute directory", stateDir)
		}
	}

	if security, ok := raw["security"].(map[string]any); ok {
		for _, key := range rejectedSecurityToggles {
			if _, present := security[key]; present {
				add("security.%s is a hard safety invariant and cannot be configured", key)
			}
		}
	}

	if webCfg, ok := raw["web"].(map[string]any); ok {
		validateWeb(webCfg, add)
	}

	notifiers, _ := raw["notifiers"].(map[string]any)
	validateNotifiers(notifiers, add)

	if watches, ok := raw["watches"].(map[string]any); ok {
		validateWatches(watches, filepath.Join(cfg.Global.RuntimeDir(), "locks"), notifierNames(notifiers), add)
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

// validateWatches checks each host-watch entry: a known check type with valid
// thresholds and a non-empty hook command (spec 2026-06-06-host-watches-disk).
// validateWeb checks the global `web` block. The UI is enabled only when `port`
// is set to an integer in 1..65535; a `web` block without `port` (or with port
// omitted) is valid and leaves the dashboard disabled, matching sermod.
func validateWeb(webCfg map[string]any, add func(string, ...any)) {
	if portRaw, present := webCfg["port"]; present {
		port, ok := scalarInt(portRaw)
		if !ok || port < 1 || port > 65535 {
			add("web.port must be an integer in 1..65535")
		}
	}
	if v, present := webCfg["address"]; present {
		if _, isStr := v.(string); !isStr {
			add("web.address must be a string")
		}
	}
	for _, key := range []string{"password", "guest_password"} {
		if v, present := webCfg[key]; present {
			if _, isStr := v.(string); !isStr {
				add("web.%s must be a string", key)
			}
		}
	}
	if v, present := webCfg["guest"]; present {
		if _, isBool := v.(bool); !isBool {
			add("web.guest must be a boolean (allow anonymous read-only access)")
		}
	}
}

// validateNotifiers checks the global `notifiers` section: each entry is a known
// type with the fields that type needs. New transports validate here too.
func validateNotifiers(notifiers map[string]any, add func(string, ...any)) {
	for _, name := range sortedKeys(notifiers) {
		entry, ok := notifiers[name].(map[string]any)
		if !ok {
			add("notifiers.%s must be a mapping", name)
			continue
		}
		switch scalarString(entry["type"]) {
		case "email":
			dsn := scalarString(entry["dsn"])
			if dsn == "" {
				add("notifiers.%s.dsn is required for an email notifier", name)
			} else if !strings.HasPrefix(dsn, "smtp://") && !strings.HasPrefix(dsn, "smtps://") {
				add("notifiers.%s.dsn must be an smtp:// or smtps:// URL", name)
			}
			if scalarString(entry["from"]) == "" {
				add("notifiers.%s.from is required for an email notifier", name)
			}
			if len(stringSlice(entry["to"])) == 0 {
				add("notifiers.%s.to must list at least one address", name)
			}
		case "slack":
			wh := scalarString(entry["webhook"])
			if wh == "" {
				add("notifiers.%s.webhook is required for a slack notifier", name)
			} else if !strings.HasPrefix(wh, "http://") && !strings.HasPrefix(wh, "https://") {
				add("notifiers.%s.webhook must be an http(s) URL", name)
			}
		case "":
			add("notifiers.%s.type is required", name)
		default:
			add("notifiers.%s.type %q is not supported (email, slack)", name, scalarString(entry["type"]))
		}
	}
}

// notifierNames returns the set of defined notifier names, for reference checks.
func notifierNames(notifiers map[string]any) map[string]struct{} {
	names := make(map[string]struct{}, len(notifiers))
	for name := range notifiers {
		names[name] = struct{}{}
	}
	return names
}

// validateNotifyRefs checks that every `then.notify` name in a watch (entry-level
// and per-metric) refers to a defined notifier.
func validateNotifyRefs(name string, entry map[string]any, notifiers map[string]struct{}, add func(string, ...any)) {
	check := func(prefix string, then any) {
		t, ok := then.(map[string]any)
		if !ok {
			return
		}
		for _, ref := range stringSlice(t["notify"]) {
			if _, ok := notifiers[ref]; !ok {
				add("%s.then.notify references unknown notifier %q", prefix, ref)
			}
		}
	}
	check("watches."+name, entry["then"])
	if metrics, ok := entry["metrics"].(map[string]any); ok {
		for _, key := range sortedKeys(metrics) {
			if m, ok := metrics[key].(map[string]any); ok {
				check(fmt.Sprintf("watches.%s.metrics.%s", name, key), m["then"])
			}
		}
	}
}

func validateWatches(watches map[string]any, locksDir string, notifiers map[string]struct{}, add func(string, ...any)) {
	for _, name := range sortedKeys(watches) {
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
		switch scalarString(check["type"]) {
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
			if validateWatchableCheck(cp, scalarString(check["type"]), check, locksDir, add) {
				validateHookBlock("watches."+name, entry, add)
			} else {
				add("watches.%s.check.type %q is not supported", name, scalarString(check["type"]))
			}
		}

		if v, present := entry["interval"]; present && !isPositiveDuration(scalarString(v)) {
			add("watches.%s.interval %q must be a valid positive duration", name, scalarString(v))
		}

		validateNotifyRefs(name, entry, notifiers, add)
		validateWatchWindow(name, entry, add)
	}
}

// validateWatchWindow checks the optional for/within window on a watch entry:
// for.cycles and within.cycles must be positive integers and within.min_matches
// (if present) a non-negative integer (spec 2026-06-06-host-watches-disk §5).
func validateWatchWindow(name string, entry map[string]any, add func(string, ...any)) {
	if f, ok := entry["for"].(map[string]any); ok {
		if c, _ := scalarInt(f["cycles"]); c <= 0 {
			add("watches.%s.for.cycles must be a positive integer", name)
		}
	}
	if wn, ok := entry["within"].(map[string]any); ok {
		if c, _ := scalarInt(wn["cycles"]); c <= 0 {
			add("watches.%s.within.cycles must be a positive integer", name)
		}
		if raw, present := wn["min_matches"]; present {
			if m, _ := scalarInt(raw); m < 0 {
				add("watches.%s.within.min_matches must be a non-negative integer", name)
			}
		}
	}
}

// validateDiskFields validates a disk check's fields at prefix (the dotted path
// to the fields container, e.g. "watches.disk-root.check" or "checks.root").
// Shared by host watches and service checks. A disk check verifies space/inodes
// and/or the mount (mounted/fstype/options/device), so at least one of the two
// must be present.
func validateDiskFields(prefix string, fields map[string]any, add addFunc) {
	if scalarString(fields["path"]) == "" {
		add("%s.path is required for a disk check", prefix)
	}
	preds := validatePresentThresholds(prefix, fields, []string{"used_pct", "free_pct", "inodes_used_pct", "inodes_free_pct", "inodes_free"}, add)
	hasMount := validateMountConditions(prefix, fields, add)
	if preds == 0 && !hasMount {
		add("%s requires a space/inode predicate (used_pct/free_pct/inodes_*) and/or a mount condition (mounted/fstype/options/device)", prefix)
	}
}

// validateMountConditions validates the optional mount fields of a disk check and
// reports whether any was present (a boolean mounted, string fstype/device, or a
// string-list options).
func validateMountConditions(prefix string, fields map[string]any, add addFunc) bool {
	active := false
	if v, present := fields["mounted"]; present {
		active = true
		if _, ok := v.(bool); !ok {
			add("%s.mounted must be a boolean", prefix)
		}
	}
	if scalarString(fields["fstype"]) != "" {
		active = true
	}
	if scalarString(fields["device"]) != "" {
		active = true
	}
	if v, present := fields["options"]; present {
		active = true
		if !isStringArray(v) {
			add("%s.options must be a non-empty list of strings", prefix)
		}
	}
	return active
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
	notify := stringSlice(then["notify"])
	if !hasHook && len(notify) == 0 {
		add("%s.then requires a hook and/or notify", prefix)
		return
	}
	if hasHook {
		list, ok := hook["command"].([]any)
		if !ok || len(list) == 0 {
			add("%s.then.hook.command must be a non-empty array", prefix)
		}
		if v, present := hook["timeout"]; present && !isPositiveDuration(scalarString(v)) {
			add("%s.then.hook.timeout %q must be a valid positive duration", prefix, scalarString(v))
		}
	}
}

// validateNetCheck validates a net interface watch: an interface and a non-empty
// metrics map, each metric with a valid condition and its own hook
// (spec 2026-06-06-net-interface-watch §4).
func validateNetCheck(name string, check, entry map[string]any, add func(string, ...any)) {
	if scalarString(check["interface"]) == "" {
		add("watches.%s.check.interface is required for a net check", name)
	}
	metrics, ok := entry["metrics"].(map[string]any)
	if !ok || len(metrics) == 0 {
		add("watches.%s.metrics is required and must be non-empty for a net check", name)
		return
	}
	for _, key := range sortedKeys(metrics) {
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
			if scalarString(m["on"]) != "change" {
				add("%s requires on: change", prefix)
			}
		case "errors":
			delta, ok := m["delta"].(map[string]any)
			if !ok {
				add("%s.delta {op, value} is required", prefix)
			} else {
				if !isValidDiskOp(scalarString(delta["op"])) {
					add("%s.delta has an invalid op %q", prefix, scalarString(delta["op"]))
				}
				if !isNumeric(scalarString(delta["value"])) {
					add("%s.delta value %q must be numeric", prefix, scalarString(delta["value"]))
				}
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
		validateMetricWindow(prefix, m, add)
	}
}

// validateEntropyFields validates an entropy check's required avail {op, value}
// threshold at prefix.
func validateEntropyFields(prefix string, fields map[string]any, add addFunc) {
	validateThresholdMap(prefix, "avail", fields["avail"], "for an entropy check", add)
}

// validateZombieFields validates a zombies check's required count {op, value}
// threshold at prefix.
func validateZombieFields(prefix string, fields map[string]any, add addFunc) {
	validateThresholdMap(prefix, "count", fields["count"], "for a zombies check", add)
}

// validateThresholdMap validates a single required {op, value} threshold field.
func validateThresholdMap(prefix, field string, raw any, suffix string, add addFunc) {
	m, ok := raw.(map[string]any)
	if !ok {
		add("%s.%s {op, value} is required %s", prefix, field, suffix)
		return
	}
	if !isValidDiskOp(scalarString(m["op"])) {
		add("%s.%s has an invalid op %q", prefix, field, scalarString(m["op"]))
	}
	if !isNumeric(scalarString(m["value"])) {
		add("%s.%s value %q must be numeric", prefix, field, scalarString(m["value"]))
	}
}

// validatePresentThresholds validates the present {op, value} predicates among
// fields and returns how many were present (it does not require any).
func validatePresentThresholds(prefix string, fieldsMap map[string]any, fields []string, add addFunc) int {
	preds := 0
	for _, field := range fields {
		raw, present := fieldsMap[field]
		if !present {
			continue
		}
		preds++
		m, ok := raw.(map[string]any)
		if !ok {
			add("%s.%s must be a mapping {op, value}", prefix, field)
			continue
		}
		if !isValidDiskOp(scalarString(m["op"])) {
			add("%s.%s has an invalid op %q", prefix, field, scalarString(m["op"]))
		}
		if !isNumeric(scalarString(m["value"])) {
			add("%s.%s value %q must be numeric", prefix, field, scalarString(m["value"]))
		}
	}
	return preds
}

// validateThresholdPreds validates a check whose body is a set of named threshold
// predicates (each {op, value}), requiring at least one of fields to be present.
// Shared by fds, conntrack and load.
func validateThresholdPreds(prefix string, fieldsMap map[string]any, fields []string, add addFunc) {
	if validatePresentThresholds(prefix, fieldsMap, fields, add) == 0 {
		add("%s requires at least one of %s", prefix, strings.Join(fields, "/"))
	}
}

// validateOomFields validates an oom check's optional delta {op, value} (the
// default fires on any OOM kill, so a bare oom check is valid).
func validateOomFields(prefix string, fields map[string]any, add addFunc) {
	delta, present := fields["delta"]
	if !present {
		return
	}
	m, ok := delta.(map[string]any)
	if !ok {
		add("%s.delta must be a mapping {op, value}", prefix)
		return
	}
	if !isValidDiskOp(scalarString(m["op"])) {
		add("%s.delta has an invalid op %q", prefix, scalarString(m["op"]))
	}
	if !isNumeric(scalarString(m["value"])) {
		add("%s.delta value %q must be numeric", prefix, scalarString(m["value"]))
	}
}

// validateCheckGate validates a check's interdependency fields: `requires` is a
// list of other check names in the same section (a check may not require itself or
// an unknown check), and `skip_when_changed` is a list of file paths.
func validateCheckGate(path, name string, entry, section map[string]any, add addFunc) {
	if v, present := entry["requires"]; present {
		reqs, ok := gateStrings(v)
		if !ok {
			add("%s.requires must be a check name or a list of check names", path)
		}
		for _, dep := range reqs {
			if dep == name {
				add("%s.requires cannot reference itself", path)
			} else if _, ok := section[dep]; !ok {
				add("%s.requires references unknown check %q", path, dep)
			}
		}
	}
	if v, present := entry["skip_when_changed"]; present {
		if _, ok := gateStrings(v); !ok {
			add("%s.skip_when_changed must be a file path or a list of file paths", path)
		}
	}
}

// gateStrings accepts a scalar string or a list of strings, returning the values
// and whether the shape is valid.
func gateStrings(v any) ([]string, bool) {
	switch t := v.(type) {
	case nil:
		return nil, true
	case string:
		if t == "" {
			return nil, true
		}
		return []string{t}, true
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s := scalarString(e); s != "" {
				out = append(out, s)
			}
		}
		return out, true
	default:
		return nil, false
	}
}

// validateHTTPFields validates an http check at prefix: a required url, and the
// optional request (method/headers/body/json) and response-assertion fields
// (expect_body/expect_json) shapes.
func validateHTTPFields(prefix string, fields map[string]any, add addFunc) {
	if scalarString(fields["url"]) == "" {
		add("%s.url is required for an http check", prefix)
	}
	if v, present := fields["method"]; present {
		s, ok := v.(string)
		if !ok {
			add("%s.method must be a string", prefix)
		} else if _, known := httpMethods[strings.ToUpper(strings.TrimSpace(s))]; !known {
			add("%s.method %q is not a standard HTTP method (GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS, TRACE, CONNECT)", prefix, s)
		}
	}
	if v, present := fields["http3"]; present {
		if h3, ok := v.(bool); !ok {
			add("%s.http3 must be a boolean", prefix)
		} else if h3 {
			// HTTP/3 runs over QUIC (TLS-only) and cannot use an HTTP proxy.
			if u := scalarString(fields["url"]); u != "" {
				if parsed, err := url.Parse(u); err != nil || parsed.Scheme != "https" {
					add("%s.http3 requires an https url", prefix)
				}
			}
			if scalarString(fields["proxy"]) != "" {
				add("%s.http3 and proxy are mutually exclusive", prefix)
			}
		}
	}
	if p := scalarString(fields["proxy"]); p != "" {
		u, err := url.Parse(p)
		if err != nil || u.Host == "" {
			add("%s.proxy %q is not a valid URL", prefix, p)
		} else {
			switch u.Scheme {
			case "http", "https", "socks5", "socks5h":
			default:
				add("%s.proxy scheme must be http, https or socks5", prefix)
			}
		}
	}
	if v, present := fields["body"]; present {
		if _, ok := v.(string); !ok {
			add("%s.body must be a string", prefix)
		}
	}
	if v, present := fields["headers"]; present {
		if _, ok := v.(map[string]any); !ok {
			add("%s.headers must be a mapping", prefix)
		}
	}
	if v, present := fields["expect_body"]; present {
		switch m := v.(type) {
		case string:
			// substring match
		case map[string]any:
			validateOpValue(prefix, "expect_body", m, add)
		default:
			add("%s.expect_body must be a string or an {op, value} mapping", prefix)
		}
	}
	if m, ok := fields["expect_status"].(map[string]any); ok {
		validateOpValue(prefix, "expect_status", m, add)
	}
	if v, present := fields["expect_latency"]; present {
		if m, ok := v.(map[string]any); ok {
			validateOpValue(prefix, "expect_latency", m, add)
		} else {
			add("%s.expect_latency must be an {op, value} mapping", prefix)
		}
	}
	if v, present := fields["expect_json"]; present {
		m, ok := v.(map[string]any)
		if !ok {
			add("%s.expect_json must be a mapping", prefix)
		} else {
			for _, path := range sortedKeys(m) {
				if cond, ok := m[path].(map[string]any); ok {
					if op := scalarString(cond["op"]); op != "" && !validJSONOp(op) {
						add("%s.expect_json.%s op %q is not one of ==, !=, >, >=, <, <=, contains", prefix, path, op)
					}
				}
			}
		}
	}
}

func validJSONOp(op string) bool {
	switch op {
	case "==", "!=", ">", ">=", "<", "<=", "contains", "=~":
		return true
	default:
		return false
	}
}

// validateOpValue validates an {op, value} comparison mapping (shared by the
// http response comparisons): op must be a known comparison operator, and value
// must be numeric for ordering ops and a valid regexp for =~.
func validateOpValue(prefix, label string, m map[string]any, add addFunc) {
	op := scalarString(m["op"])
	if _, ok := compareOps[op]; !ok {
		add("%s.%s op %q is not one of ==, !=, >, >=, <, <=, =~", prefix, label, op)
		return
	}
	value := scalarString(m["value"])
	switch op {
	case ">", ">=", "<", "<=":
		if !isNumeric(value) {
			add("%s.%s value %q must be numeric for op %s", prefix, label, value, op)
		}
	case "=~":
		if _, err := regexp.Compile(value); err != nil {
			add("%s.%s value is not a valid regexp: %v", prefix, label, err)
		}
	}
}

// validatePortsFields validates a ports check at prefix: a parseable `ports` spec
// (list + ranges) and the enumerated expect/match values.
func validatePortsFields(prefix string, fields map[string]any, add addFunc) {
	if err := validatePortSpec(scalarString(fields["ports"])); err != "" {
		add("%s.ports %s", prefix, err)
	}
	if v := scalarString(fields["expect"]); v != "" && v != "open" && v != "closed" && v != "any" {
		add("%s.expect must be open, closed or any", prefix)
	}
	if v := scalarString(fields["match"]); v != "" && v != "all" && v != "any" && v != "none" {
		add("%s.match must be all, any or none", prefix)
	}
	if v, present := fields["on_change"]; present {
		if _, ok := v.(bool); !ok {
			add("%s.on_change must be a boolean", prefix)
		}
	}
}

// validatePortSpec returns "" when spec is a valid comma-separated list of ports
// and inclusive ranges (e.g. "80,443,1024-4000"), else a short reason.
func validatePortSpec(spec string) string {
	if strings.TrimSpace(spec) == "" {
		return "is required (e.g. \"80,443,1024-4000\")"
	}
	found := false
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(tok, "-")
		start, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil {
			return fmt.Sprintf("has an invalid port %q", tok)
		}
		end := start
		if isRange {
			end, err = strconv.Atoi(strings.TrimSpace(hi))
			if err != nil {
				return fmt.Sprintf("has an invalid range %q", tok)
			}
		}
		if start < 1 || end > 65535 || start > end {
			return fmt.Sprintf("range %q is out of 1..65535", tok)
		}
		found = true
	}
	if !found {
		return "is required (e.g. \"80,443,1024-4000\")"
	}
	return ""
}

// validateWatchableCheck validates the fields of a single-shot service check used
// as a host watch and reports whether the type is watchable. service/metric/
// process are excluded: they need per-service context (backend status, a metric
// sampler, process discovery) that the watch builder does not provide.
func validateWatchableCheck(prefix, typ string, fields map[string]any, locksDir string, add addFunc) bool {
	switch typ {
	case "tcp":
		if _, ok := scalarInt(fields["port"]); !ok {
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
		if scalarString(fields["path"]) == "" {
			add("%s.path is required for a binary check", prefix)
		}
	case "libraries":
		if scalarString(fields["binary"]) == "" {
			add("%s.binary is required for a libraries check", prefix)
		}
	case "file_exists":
		p := scalarString(fields["path"])
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

// validateLoadFields validates a load check at prefix: an optional boolean
// per_cpu and at least one load1/load5/load15 threshold.
func validateLoadFields(prefix string, fields map[string]any, add addFunc) {
	if v, present := fields["per_cpu"]; present {
		if _, ok := v.(bool); !ok {
			add("%s.per_cpu must be a boolean", prefix)
		}
	}
	validateThresholdPreds(prefix, fields, []string{"load1", "load5", "load15"}, add)
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
	for _, key := range sortedKeys(metrics) {
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
				if !isValidDiskOp(scalarString(mm["op"])) {
					add("%s.%s has an invalid op %q", prefix, field, scalarString(mm["op"]))
				}
				if !isNumeric(scalarString(mm["value"])) {
					add("%s.%s value %q must be numeric", prefix, field, scalarString(mm["value"]))
				}
			}
			if preds == 0 {
				add("%s requires at least one of used_pct/free_pct/free_bytes", prefix)
			}
		case "io":
			delta, ok := m["delta"].(map[string]any)
			if !ok {
				add("%s.delta {op, value} is required", prefix)
			} else {
				if !isValidDiskOp(scalarString(delta["op"])) {
					add("%s.delta has an invalid op %q", prefix, scalarString(delta["op"]))
				}
				if !isNumeric(scalarString(delta["value"])) {
					add("%s.delta value %q must be numeric", prefix, scalarString(delta["value"]))
				}
			}
		default:
			add("%s is not a supported swap metric (usage, io)", prefix)
		}
		validateHookBlock(prefix, m, add)
		validateMetricWindow(prefix, m, add)
	}
}

// validateStateMetric validates a state metric condition shared by net/icmp:
// expect up|down OR on: change.
func validateStateMetric(prefix string, m map[string]any, add func(string, ...any)) {
	exp := scalarString(m["expect"])
	onChange := scalarString(m["on"]) == "change"
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
	if scalarString(check["host"]) == "" {
		add("watches.%s.check.host is required for an icmp check", name)
	}
	if v, present := check["count"]; present {
		if n, ok := scalarInt(v); !ok || n <= 0 {
			add("watches.%s.check.count must be a positive integer", name)
		}
	}
	metrics, ok := entry["metrics"].(map[string]any)
	if !ok || len(metrics) == 0 {
		add("watches.%s.metrics is required and must be non-empty for an icmp check", name)
		return
	}
	for _, key := range sortedKeys(metrics) {
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
				if !isValidDiskOp(scalarString(th["op"])) {
					add("%s.threshold has an invalid op %q", prefix, scalarString(th["op"]))
				}
				if !isNumeric(scalarString(th["value"])) {
					add("%s.threshold value %q must be numeric", prefix, scalarString(th["value"]))
				}
			}
			if hasC {
				if !isNumeric(scalarString(ch["delta"])) {
					add("%s.change delta %q must be numeric", prefix, scalarString(ch["delta"]))
				}
			}
		default:
			add("%s is not a supported icmp metric (state, latency)", prefix)
		}
		validateHookBlock(prefix, m, add)
		validateMetricWindow(prefix, m, add)
	}
}

// validateFileCheck validates a file watch: a path, an optional boolean
// recursive, and at least one attribute condition (size threshold/change,
// permissions/owner on change, existence on delete), plus the entry's hook.
func validateFileCheck(name string, check, entry map[string]any, add func(string, ...any)) {
	if scalarString(check["path"]) == "" {
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
		if scalarString(sz["on"]) != "change" {
			if !isValidDiskOp(scalarString(sz["op"])) || !isNumeric(scalarString(sz["value"])) {
				add("watches.%s.check.size requires on: change or {op, value} with a numeric value", name)
			}
		}
	}
	for _, attr := range []string{"permissions", "owner"} {
		if m, ok := check[attr].(map[string]any); ok {
			conds++
			if scalarString(m["on"]) != "change" {
				add("watches.%s.check.%s requires on: change", name, attr)
			}
		}
	}
	if e, ok := check["existence"].(map[string]any); ok {
		conds++
		if scalarString(e["on"]) != "delete" {
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
	if scalarString(check["name"]) == "" {
		add("watches.%s.check.name is required for a process check", name)
	}
	conds := 0
	if v, present := check["for"]; present {
		conds++
		if !isPositiveDuration(scalarString(v)) {
			add("watches.%s.check.for %q must be a valid positive duration", name, scalarString(v))
		}
	}
	for _, attr := range []string{"cpu", "memory", "io"} {
		m, ok := check[attr].(map[string]any)
		if !ok {
			continue
		}
		conds++
		if !isValidDiskOp(scalarString(m["op"])) || !isNumeric(scalarString(m["value"])) {
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

// validateMetricWindow validates a per-metric for/within window using the same
// rules as validateWatchWindow but with a metric-scoped prefix.
func validateMetricWindow(prefix string, m map[string]any, add func(string, ...any)) {
	if f, ok := m["for"].(map[string]any); ok {
		if c, _ := scalarInt(f["cycles"]); c <= 0 {
			add("%s.for.cycles must be a positive integer", prefix)
		}
	}
	if wn, ok := m["within"].(map[string]any); ok {
		if c, _ := scalarInt(wn["cycles"]); c <= 0 {
			add("%s.within.cycles must be a positive integer", prefix)
		}
		if raw, present := wn["min_matches"]; present {
			if mm, _ := scalarInt(raw); mm < 0 {
				add("%s.within.min_matches must be a non-negative integer", prefix)
			}
		}
	}
}

func isValidDiskOp(op string) bool {
	switch op {
	case ">=", ">", "<=", "<", "==", "!=":
		return true
	default:
		return false
	}
}

func isNumeric(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// validateConnFields validates a connection-protocol check (mysql, …): a user
// is required (password is optional and may come from the environment), the
// port must be numeric when present, and tls must be a boolean or one of the
// known string modes.
func validateConnFields(prefix string, fields map[string]any, requireUser bool, add addFunc) {
	if requireUser && scalarString(fields["user"]) == "" {
		add("%s.user is required for a connection check", prefix)
	}
	if v, present := fields["port"]; present && !isNumeric(scalarString(v)) {
		add("%s.port %q must be numeric", prefix, scalarString(v))
	}
	if v, present := fields["tls"]; present {
		switch t := v.(type) {
		case bool:
			// fine
		case string:
			switch strings.ToLower(strings.TrimSpace(t)) {
			case "true", "false", "yes", "no", "on", "off", "required", "skip-verify", "skip_verify", "insecure",
				// PostgreSQL sslmodes
				"disable", "require", "prefer", "verify-ca", "verify-full":
			default:
				add("%s.tls %q must be a boolean, skip-verify, or a valid sslmode", prefix, t)
			}
		default:
			add("%s.tls must be a boolean or a string (true/false/skip-verify)", prefix)
		}
	}
	// expect: optional response assertions (field -> value | {op, value}),
	// compared against the probe's version / Extra fields.
	if v, present := fields["expect"]; present {
		m, ok := v.(map[string]any)
		if !ok {
			add("%s.expect must be a mapping of field -> value or {op, value}", prefix)
		} else {
			for _, field := range sortedKeys(m) {
				if cond, ok := m[field].(map[string]any); ok {
					validateOpValue(prefix, "expect."+field, cond, add)
				}
			}
		}
	}
	if v, present := fields["expect_latency"]; present {
		if m, ok := v.(map[string]any); ok {
			validateOpValue(prefix, "expect_latency", m, add)
		} else {
			add("%s.expect_latency must be an {op, value} mapping", prefix)
		}
	}
	for _, key := range []string{"on_change", "on_version_change"} {
		if v, present := fields[key]; present {
			if _, ok := v.(bool); !ok {
				add("%s.%s must be a boolean", prefix, key)
			}
		}
	}
}

func validateDocuments(cfg *Config) []Issue {
	var issues []Issue
	profileCount := map[string]int{}
	serviceCount := map[string]int{}

	for _, doc := range cfg.docs {
		scope := documentScope(doc)
		if d, present := doc.Body["description"]; present {
			if _, ok := d.(string); !ok {
				issues = append(issues, Issue{Scope: scope, Msg: "description must be a string"})
			}
		}
		if d, present := doc.Body["display_name"]; present {
			if _, ok := d.(string); !ok {
				issues = append(issues, Issue{Scope: scope, Msg: "display_name must be a string"})
			}
		}
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
		if !validDocumentName(doc.Name) {
			issues = append(issues, Issue{Scope: scope, Msg: fmt.Sprintf("document name %q must be a simple name without path separators", doc.Name)})
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

func validDocumentName(name string) bool {
	return name != "." && name != ".." && !strings.Contains(name, "/") && !strings.Contains(name, `\`)
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

	if v, present := tree["interval"]; present && !isPositiveDuration(scalarString(v)) {
		add("interval %q must be a valid positive duration", scalarString(v))
	}

	if mode, present := tree["monitor"]; present {
		s, isStr := mode.(string)
		if _, ok := validMonitorModes[s]; !isStr || !ok {
			add("monitor %q is not one of enabled, disabled, previous", scalarString(mode))
		}
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
			if !validExpectStatus(value) {
				add("%s = %q must resolve to a valid HTTP status, class (2xx) or list", path, value)
			}
		}
	})

	locksDir := filepath.Join(runtime, "locks")
	validateCheckSection(tree, "checks", locksDir, add)
	validateCheckSection(tree, "preflight", locksDir, add)
	validateCheckSection(tree, "postflight", locksDir, add)
	validateProcesses(tree, add)
	validateStopPolicy(tree, add)
	validatePolicyExtras(tree, add)
	validateServiceField(tree, add)
	validateCommands(tree, add)
	validateRules(tree, add)

	return issues
}

// ---- section 30: checks, stop_policy, policy, service, rules ----

var validMonitorModes = set(MonitorEnabled, MonitorDisabled, MonitorPrevious)

// knownCheckTypes are the single-shot check types valid in a service's
// checks:/preflight:/postflight: sections (and referenceable from rules). The
// host-resource checks (disk…oom) are shared with host watches; the multi-target
// watch types (net, icmp, swap, file, process) stay watch-only because they fire
// per-metric/per-target rather than producing one Result. Keep this in step with
// internal/checks buildCheck and the watch validation (section: unified checks).
var knownCheckTypes = set("tcp", "ports", "http", "command", "service", "file_exists", "binary", "process", "metric", "libraries", "count",
	"disk", "autofs", "load", "fds", "conntrack", "entropy", "zombies", "oom", "cert", "sqlite", "sqlite3", "sql", "size", "websocket", "ws")
var countKinds = set("any", "file", "dir", "symlink")
var serviceStates = set("active", "inactive", "failed", "unknown")
var processStates = set("running", "zombie", "absent")
var validActions = set("restart", "start", "stop", "alert", "block")
var metricOps = set(">", ">=", "<", "<=", "==", "!=")

// httpMethods are the standard HTTP request methods an http check may use.
var httpMethods = set("GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "TRACE", "CONNECT")
var sqlEngines = set("mysql", "mariadb", "postgres", "postgresql", "sqlite", "sqlite3")

// compareOps is the operator set shared by the sql check and the http response
// comparisons (expect_body / expect_status / expect_latency).
var compareOps = set("==", "!=", ">", ">=", "<", "<=", "=~")
var metricCatalog = map[string]map[string]struct{}{
	"service": set("memory", "cpu", "process_count", "io", "io_read", "io_write", "fds", "threads"),
	"system":  set("total_memory", "total_cpu", "load1", "load5", "load15"),
}

// metricForms records which value forms each metric exposes (section 12/14), so
// a threshold's form can be checked against the metric.
type metricForm struct{ absolute, percent bool }

var metricForms = map[string]metricForm{
	"memory":        {absolute: true, percent: true},
	"cpu":           {percent: true},
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
		// A per-check interval runs the check every N cycles (N rounded from
		// interval/resolution). It must be a positive duration; the daemon warns at
		// startup if it is below the resolution or not an exact multiple.
		if v, present := entry["interval"]; present && !isPositiveDuration(scalarString(v)) {
			add("%s.interval %q must be a valid positive duration", path, scalarString(v))
		}
		validateCheckGate(path, name, entry, entries, add)
		typ := scalarString(entry["type"])
		if typ == "" {
			add("%s has no type", path)
			continue
		}
		if _, known := knownCheckTypes[typ]; !known {
			// A connection-protocol check (mysql, …): the type names a protocol
			// in the conn registry, validated generically below.
			if proto, isProto := conn.Lookup(typ); isProto {
				validateConnFields(path, entry, proto.RequiresUser(), add)
				continue
			}
			add("%s has unknown type %q", path, typ)
			continue
		}
		switch typ {
		case "http":
			validateHTTPFields(path, entry, add)
		case "ports":
			validatePortsFields(path, entry, add)
		case "command":
			if !isStringArray(entry["command"]) {
				add("%s command must be an array, not a shell string", path)
			}
			if v, present := entry["expect_exit"]; present {
				if _, ok := scalarInt(v); !ok {
					add("%s expect_exit must be an integer", path)
				}
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
		case "count":
			validateCount(entry, path, add)
		case "disk":
			validateDiskFields(path, entry, add)
		case "autofs":
			validateAutofsFields(path, entry, add)
		case "load":
			validateLoadFields(path, entry, add)
		case "fds":
			validateThresholdPreds(path, entry, []string{"used_pct", "free", "allocated"}, add)
		case "conntrack":
			validateThresholdPreds(path, entry, []string{"used_pct", "free", "count"}, add)
		case "entropy":
			validateEntropyFields(path, entry, add)
		case "zombies":
			validateZombieFields(path, entry, add)
		case "oom":
			validateOomFields(path, entry, add)
		case "cert":
			validateCertFields(path, entry, add)
		case "sqlite", "sqlite3":
			if scalarString(entry["path"]) == "" {
				add("%s.path is required for a sqlite check", path)
			}
		case "sql":
			validateSQLFields(path, entry, add)
		case "size":
			validateSizeFields(path, entry, add)
		case "websocket", "ws":
			validateWebsocketFields(path, entry, add)
		}
	}
}

// validateWebsocketFields validates a websocket check: a required url with a
// ws/wss/http/https scheme.
func validateWebsocketFields(prefix string, fields map[string]any, add addFunc) {
	raw := scalarString(fields["url"])
	if raw == "" {
		add("%s.url is required for a websocket check", prefix)
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		add("%s.url %q is not a valid URL", prefix, raw)
		return
	}
	switch u.Scheme {
	case "ws", "wss", "http", "https":
	default:
		add("%s.url scheme must be ws, wss, http or https", prefix)
	}
}

// validateAutofsFields validates an autofs check: an optional count {op, value}
// predicate, mutually exclusive with path.
func validateAutofsFields(prefix string, fields map[string]any, add addFunc) {
	count, hasCount := fields["count"].(map[string]any)
	if !hasCount {
		return
	}
	if scalarString(fields["path"]) != "" {
		add("%s: path and count are mutually exclusive", prefix)
	}
	op := scalarString(count["op"])
	if _, ok := metricOps[op]; !ok {
		add("%s.count.op %q is not one of >, >=, <, <=, ==, !=", prefix, op)
	}
	if !isNumeric(scalarString(count["value"])) {
		add("%s.count.value must be numeric", prefix)
	}
}

// validateSizeFields validates a size (growth) check: a required path, a
// positive parseable grow_by byte size and a positive within duration.
func validateSizeFields(prefix string, fields map[string]any, add addFunc) {
	if scalarString(fields["path"]) == "" {
		add("%s.path is required for a size check", prefix)
	}
	gb := scalarString(fields["grow_by"])
	if gb == "" {
		add("%s.grow_by is required for a size check (e.g. 1GB)", prefix)
	} else if n, err := humanize.ParseBytes(gb); err != nil || n == 0 {
		add("%s.grow_by %q must be a positive size (e.g. 1GB, 500MB)", prefix, gb)
	}
	w := scalarString(fields["within"])
	if w == "" {
		add("%s.within is required for a size check (e.g. 1h)", prefix)
	} else if !isPositiveDuration(w) {
		add("%s.within %q must be a valid positive duration", prefix, w)
	}
}

// validateSQLFields validates a sql check: a known engine, a query, a valid op
// and a value. For numeric ops the value must be numeric; for =~ it must be a
// valid regexp. mysql/postgres require a user; sqlite requires a path.
func validateSQLFields(prefix string, fields map[string]any, add addFunc) {
	engine := scalarString(fields["engine"])
	if _, ok := sqlEngines[engine]; !ok {
		add("%s.engine must be one of mysql, mariadb, postgres, postgresql, sqlite", prefix)
	}
	if scalarString(fields["query"]) == "" {
		add("%s.query is required for a sql check", prefix)
	}
	op := scalarString(fields["op"])
	if _, ok := compareOps[op]; !ok {
		add("%s.op %q is not one of ==, !=, >, >=, <, <=, =~", prefix, op)
	}
	value := scalarString(fields["value"])
	switch op {
	case ">", ">=", "<", "<=":
		if !isNumeric(value) {
			add("%s.value %q must be numeric for op %s", prefix, value, op)
		}
	case "=~":
		if _, err := regexp.Compile(value); err != nil {
			add("%s.value is not a valid regexp: %v", prefix, err)
		}
	}
	switch engine {
	case "sqlite", "sqlite3":
		if scalarString(fields["path"]) == "" {
			add("%s.path is required for a sqlite sql check", prefix)
		}
	case "mysql", "mariadb", "postgres", "postgresql":
		if scalarString(fields["user"]) == "" {
			add("%s.user is required for a %s sql check", prefix, engine)
		}
	}
}

// validateCertFields validates a cert check at prefix: a required host, optional
// port (1..65535), optional positive expires_in_days, and boolean toggles. New
// certificate conditions add here.
func validateCertFields(prefix string, fields map[string]any, add addFunc) {
	host := scalarString(fields["host"])
	path := scalarString(fields["path"])
	switch {
	case host == "" && path == "":
		add("%s requires a host or a path", prefix)
	case host != "" && path != "":
		add("%s.host and %s.path are mutually exclusive", prefix, prefix)
	}
	if v, present := fields["port"]; present {
		if n, ok := scalarInt(v); !ok || n < 1 || n > 65535 {
			add("%s.port must be an integer in 1..65535", prefix)
		}
	}
	if v, present := fields["expires_in_days"]; present {
		if n, ok := scalarInt(v); !ok || n < 1 {
			add("%s.expires_in_days must be a positive integer", prefix)
		}
	}
	for _, key := range []string{"on_algorithm_change", "on_issuer_change", "on_change", "verify"} {
		if v, present := fields[key]; present {
			if _, ok := v.(bool); !ok {
				add("%s.%s must be a boolean", prefix, key)
			}
		}
	}
}

// validateCount checks a count entry: a path, an optional `of` kind, an optional
// boolean `recursive`, and a required numeric threshold (op + value).
func validateCount(entry map[string]any, path string, add addFunc) {
	if scalarString(entry["path"]) == "" {
		add("%s count check requires a path", path)
	}
	if of := scalarString(entry["of"]); of != "" {
		if _, ok := countKinds[of]; !ok {
			add("%s count `of` %q is not one of any, file, dir, symlink", path, of)
		}
	}
	if v, present := entry["recursive"]; present {
		if _, ok := v.(bool); !ok {
			add("%s count recursive must be a boolean", path)
		}
	}
	if op := scalarString(entry["op"]); !isValidDiskOp(op) {
		add("%s count check requires a valid op (>=, >, <=, <, ==, !=)", path)
	}
	if !isNumeric(scalarString(entry["value"])) {
		add("%s count check value %q must be numeric", path, scalarString(entry["value"]))
	}
}

func validateStopPolicy(tree map[string]any, add addFunc) {
	sp, ok := tree["stop_policy"].(map[string]any)
	if !ok {
		return
	}
	for _, field := range []string{"graceful_timeout", "term_timeout", "kill_timeout"} {
		if v, present := sp[field]; present && !isPositiveDuration(scalarString(v)) {
			add("stop_policy.%s %q must be a valid positive duration", field, scalarString(v))
		}
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

func validateProcesses(tree map[string]any, add addFunc) {
	processes, ok := tree["processes"].(map[string]any)
	if !ok {
		return
	}
	for _, name := range sortedKeys(processes) {
		path := "processes." + name
		entry, ok := processes[name].(map[string]any)
		if !ok {
			add("%s must be a mapping", path)
			continue
		}
		switch typ := scalarString(entry["type"]); typ {
		case "pidfile":
			if scalarString(entry["path"]) == "" {
				add("%s.path is required for a pidfile selector", path)
			}
		case "command_match":
			if scalarString(entry["exe"]) == "" || scalarString(entry["user"]) == "" {
				add("%s command_match requires both exe and user", path)
			}
		case "":
			add("%s.type is required", path)
		default:
			add("%s.type %q is not one of pidfile, command_match", path, typ)
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

// validateCommands checks the optional `commands` section: each entry uses array
// form with an optional valid duration timeout (section 30). The engine never
// runs these; they are informational metadata.
func validateCommands(tree map[string]any, add addFunc) {
	commands, ok := tree["commands"].(map[string]any)
	if !ok {
		return
	}
	for _, name := range sortedKeys(commands) {
		entry, ok := commands[name].(map[string]any)
		if !ok {
			add("commands.%s must be a mapping", name)
			continue
		}
		if !isStringArray(entry["command"]) {
			add("commands.%s command must be an array, not a shell string", name)
		}
		if v, present := entry["timeout"]; present && !isPositiveDuration(scalarString(v)) {
			add("commands.%s timeout %q must be a valid positive duration", name, scalarString(v))
		}
	}
}

// validateServiceField checks the `service` field: a scalar unit name, a per-init
// map of systemd/openrc candidate lists, or the legacy { name: ... } shorthand.
func validateServiceField(tree map[string]any, add addFunc) {
	s, present := tree["service"]
	if !present {
		return
	}
	switch v := s.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			add("service must not be empty")
		}
	case map[string]any:
		hasInit, hasName := false, false
		for _, k := range sortedKeys(v) {
			switch k {
			case "systemd", "openrc":
				hasInit = true
				if len(stringSlice(v[k])) == 0 {
					add("service.%s must be a non-empty list", k)
				}
			case "name":
				hasName = true
				if scalarString(v["name"]) == "" {
					add("service.name must not be empty")
				}
			default:
				add("service key %q is not one of systemd, openrc, name", k)
			}
		}
		if hasInit && hasName {
			add("service must not mix name with systemd/openrc")
		}
	default:
		add("service must be a unit name or a per-init map (systemd/openrc)")
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
		actions := ruleActions(then)
		isGuard := rtype == "guard"
		blocks := stringSlice(entry["blocks"])
		hasBlock := false
		for _, act := range actions {
			if act.typ != "" {
				if _, ok := validActions[act.typ]; !ok {
					add("%s then.action %q is not one of restart, start, stop, alert, block", path, act.typ)
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
	case "changed":
		if m, ok := node["changed"].(map[string]any); !ok || scalarString(m["path"]) == "" {
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
				out = append(out, valAction{typ: scalarString(m["type"]), message: scalarString(m["message"])})
			}
		}
		return out
	}
	return []valAction{{typ: scalarString(then["action"]), message: scalarString(then["message"])}}
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

// validExpectStatus accepts a single status (100..599) or a class like "2xx".
// A list is validated element-by-element by walkScalars.
func validExpectStatus(value string) bool {
	if len(value) == 3 && (value[1] == 'x' || value[1] == 'X') && (value[2] == 'x' || value[2] == 'X') && value[0] >= '1' && value[0] <= '5' {
		return true
	}
	n, err := strconv.Atoi(value)
	return err == nil && n >= 100 && n <= 599
}

func isValidBackend(b string) bool {
	_, ok := validBackends[b]
	return ok
}

func isPositiveDuration(s string) bool {
	d, err := time.ParseDuration(s)
	return err == nil && d > 0
}

func isNonNegativeDuration(s string) bool {
	d, err := time.ParseDuration(s)
	return err == nil && d >= 0
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
