package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/conn"
)

// validateDiskFields validates a storage check's fields at prefix (the dotted path
// to the fields container, e.g. "watches.storage-root.check" or "checks.root").
// Shared by host watches and service checks. A storage check verifies space/inodes
// and/or whether the path is mounted, so at least one of the two must be present.
func validateDiskFields(prefix string, fields map[string]any, add addFunc) {
	if cfgval.String(fields["path"]) == "" {
		add("%s.path is required for a storage check", prefix)
	}
	preds := validatePresentThresholds(prefix, fields, checks.DiskPredFields, add)
	hasMount := validateMountConditions(prefix, fields, add)
	if preds == 0 && !hasMount {
		add("%s requires a space/inode predicate (used_pct/free_pct/used_bytes/free_bytes/inodes_*) and/or a mount condition (mounted)", prefix)
	}
}

// validateMountConditions validates the optional mount predicate of a storage
// check and reports whether it was present.
func validateMountConditions(prefix string, fields map[string]any, add addFunc) bool {
	active := false
	if v, present := fields["mounted"]; present {
		active = true
		if _, ok := v.(bool); !ok {
			add("%s.mounted must be a boolean", prefix)
		}
	}
	for _, field := range []string{"fstype", "device", "options"} {
		if _, present := fields[field]; present {
			add("%s.%s is not supported for a storage check; use mounted to verify the mount point", prefix, field)
		}
	}
	return active
}

// validateOpNumeric validates an already-extracted {op, value} threshold map (a
// disk-style comparison op and a numeric value) at the dotted label. It is the
// shared core of every delta/threshold/predicate check.
func validateOpNumeric(label string, m map[string]any, add addFunc) {
	validateDiskOp(label, m, add)
	if !isNumeric(cfgval.String(m["value"])) {
		add("%s value %q must be numeric", label, cfgval.String(m["value"]))
	}
}

func validateOpByteSize(label string, m map[string]any, add addFunc) {
	validateDiskOp(label, m, add)
	if _, ok := cfgval.ByteSize(m["value"]); !ok {
		add("%s value %q must include a size suffix (K, M, G or T; e.g. 10G, 500M)", label, cfgval.String(m["value"]))
	}
}

func validateOpPercent(label string, m map[string]any, add addFunc) {
	validateDiskOp(label, m, add)
	if !isPercentValue(cfgval.String(m["value"])) {
		add("%s value %q must be a percentage in 0..100 (e.g. 90 or 90%%)", label, cfgval.String(m["value"]))
	}
}

// validateDiskOp adds an error when the {op} of an already-extracted threshold
// map is not one of the comparison operators the disk-style checks share. It is
// the op-validation prologue every validateOp* helper repeats.
func validateDiskOp(label string, m map[string]any, add addFunc) {
	if op := cfgval.String(m["op"]); !isValidDiskOp(op) {
		add("%s has an invalid op %q", label, op)
	}
}

func isPercentValue(s string) bool {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	n, err := strconv.ParseFloat(s, 64)
	return err == nil && n >= 0 && n <= 100
}

// validatePresentThresholds validates the present {op, value} predicates among
// fields and returns how many were present (it does not require any). Each
// value is validated by its field's form — `*_bytes` requires a size suffix,
// `*_pct` accepts a number or a trailing % in 0..100, anything else is plain
// numeric — the same grammar the runtime parser (checks.parseLevelPreds) uses.
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
		switch {
		case strings.HasSuffix(field, "_bytes"):
			validateOpByteSize(prefix+"."+field, m, add)
		case strings.HasSuffix(field, "_pct"):
			validateOpPercent(prefix+"."+field, m, add)
		default:
			validateOpNumeric(prefix+"."+field, m, add)
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
	validateOpNumeric(prefix+".delta", m, add)
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
			if s := cfgval.String(e); s != "" {
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
	if cfgval.String(fields["url"]) == "" {
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
			if u := cfgval.String(fields["url"]); u != "" {
				if parsed, err := url.Parse(u); err != nil || parsed.Scheme != "https" {
					add("%s.http3 requires an https url", prefix)
				}
			}
			if cfgval.String(fields["proxy"]) != "" {
				add("%s.http3 and proxy are mutually exclusive", prefix)
			}
		}
	}
	if p := cfgval.String(fields["proxy"]); p != "" {
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
			for _, path := range slices.Sorted(maps.Keys(m)) {
				if cond, ok := m[path].(map[string]any); ok {
					if op := cfgval.String(cond["op"]); op != "" && !cfgval.IsAssertOp(op) {
						add("%s.expect_json.%s op %q is not one of ==, !=, >, >=, <, <=, contains, =~", prefix, path, op)
					}
				}
			}
		}
	}
}

// validateOutputExpectation validates an expect_stdout/expect_stderr field, shared
// by the command check and watch hooks: a string substring, an {op, value}
// comparison with a valid operator, or absent. Any other shape is rejected.
func validateOutputExpectation(prefix, field string, v any, add addFunc) {
	switch t := v.(type) {
	case nil, string:
		// absent or a substring expectation — always valid
	case map[string]any:
		validateOpValue(prefix, field, t, add)
	default:
		add("%s.%s must be a string substring or an {op, value} mapping", prefix, field)
	}
}

// validateOpValue validates an {op, value} comparison mapping (shared by the
// http response comparisons): op must be a known comparison operator, and value
// must be numeric for ordering ops and a valid regexp for =~.
func validateOpValue(prefix, label string, m map[string]any, add addFunc) {
	op := cfgval.String(m["op"])
	if !cfgval.IsAssertOp(op) {
		add("%s.%s op %q is not one of ==, !=, >, >=, <, <=, contains, =~", prefix, label, op)
		return
	}
	value := cfgval.String(m["value"])
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
	if err := validatePortSpec(cfgval.String(fields["ports"])); err != "" {
		add("%s.ports %s", prefix, err)
	}
	if v := cfgval.String(fields["expect"]); v != "" && v != "open" && v != "closed" && v != "any" {
		add("%s.expect must be open, closed or any", prefix)
	}
	if v := cfgval.String(fields["match"]); v != "" && v != "all" && v != "any" && v != "none" {
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

// validateLoadFields validates a load check at prefix: an optional boolean
// per_cpu and at least one load1/load5/load15 threshold.
func validateLoadFields(prefix string, fields map[string]any, add addFunc) {
	if v, present := fields["per_cpu"]; present {
		if _, ok := v.(bool); !ok {
			add("%s.per_cpu must be a boolean", prefix)
		}
	}
	validateThresholdPreds(prefix, fields, checks.LoadPredFields, add)
}

// validateHdparmFields validates an hdparm check: a required device and at least
// one of the read/cached {op, value} throughput predicates.
func validateHdparmFields(prefix string, fields map[string]any, add addFunc) {
	if cfgval.String(fields["device"]) == "" {
		add("%s.device is required for an hdparm check", prefix)
	}
	if validatePresentThresholds(prefix, fields, checks.HdparmPredFields, add) == 0 {
		add("%s requires at least one of read/cached {op, value}", prefix)
	}
}

// validateSmartFields validates a smart check: a required device and any of the
// optional {op, value} attribute predicates (without one, it alerts on a failed
// SMART health verdict).
func validateSmartFields(prefix string, fields map[string]any, add addFunc) {
	if cfgval.String(fields["device"]) == "" {
		add("%s.device is required for a smart check", prefix)
	}
	validatePresentThresholds(prefix, fields, checks.SmartPredFields, add)
}

// isValidDiskOp reports whether op is one of the comparison operators shared by
// every {op, value} threshold — the single set in cfgval, shared with the
// runtime builders so the two grammars cannot drift apart.
func isValidDiskOp(op string) bool {
	return cfgval.IsCompareOp(op)
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
	if requireUser && cfgval.String(fields["user"]) == "" {
		add("%s.user is required for a connection check", prefix)
	}
	// The same 1..65535 range walkScalars enforces on resolved services, so a
	// connection check behaves identically as a host watch.
	if v, present := fields["port"]; present {
		if n, ok := cfgval.Int(v); !ok || n < 1 || n > 65535 {
			add("%s.port %q must be an integer in 1..65535", prefix, cfgval.String(v))
		}
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
			for _, field := range slices.Sorted(maps.Keys(m)) {
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

// knownCheckTypes are the single-shot check types valid in a service's
// checks:/preflight:/postflight: sections (and referenceable from rules), taken
// from the checks package so validation and the builder share one list
// (section: unified checks). net/icmp/swap are usable here in their
// single-metric form (an explicit `metric:` producing one Result); only their
// multi-metric `metrics:` map shape and the `file` watch stay watch-only.
var knownCheckTypes = set(checks.SingleShotCheckTypes...)
var countKinds = set("any", "file", "dir", "symlink")

// httpMethods are the standard HTTP request methods an http check may use.
var httpMethods = set("GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "TRACE", "CONNECT")
var sqlEngines = set("mysql", "mariadb", "postgres", "postgresql", "sqlite", "sqlite3")

// validateCheckSection validates a checks/preflight/postflight section: known
// types, optional booleans, command array form, valid service/process states,
// metric grammar, and that file_exists never points at Sermo's own lock dir.
func validateCheckSection(tree map[string]any, section, locksDir string, add addFunc) {
	entries, ok := tree[section].(map[string]any)
	if !ok {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(entries)) {
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
		if v, present := entry["interval"]; present && !isPositiveDuration(cfgval.String(v)) {
			add("%s.interval %q must be a valid positive duration", path, cfgval.String(v))
		}
		validateCheckGate(path, name, entry, entries, add)
		typ := cfgval.String(entry["type"])
		if typ == "" {
			add("%s has no type", path)
			continue
		}
		if !validateSingleShotCheckFields(path, typ, entry, locksDir, add) {
			add("%s has unknown type %q", path, typ)
			continue
		}
	}
}

// validateAnalyze statically validates a command check's resolved `analyze`
// block. It runs after expandAnalyze, so `use`/`silence` are gone and only the
// flat `rules` list remains (unknown-set/silence errors are raised during
// resolution). It checks each rule's id (present, unique), severity
// (error|warning|ok), stream (stdout|stderr|both or empty), and that `match`
// compiles as a regular expression.
func validateAnalyze(path string, entry map[string]any, add addFunc) {
	analyze, ok := entry["analyze"].(map[string]any)
	if !ok {
		if _, present := entry["analyze"]; present {
			add("%s.analyze must be a mapping", path)
		}
		return
	}
	rules, ok := analyze["rules"].([]any)
	if !ok {
		if _, present := analyze["rules"]; present {
			add("%s.analyze.rules must be a list", path)
		}
		return
	}
	seen := map[string]bool{}
	for i, item := range rules {
		rm, ok := item.(map[string]any)
		if !ok {
			add("%s.analyze rule %d must be a mapping", path, i)
			continue
		}
		id := cfgval.AsString(rm["id"])
		if id == "" {
			add("%s.analyze rule %d is missing an id", path, i)
		} else if seen[id] {
			add("%s.analyze has a duplicate rule id %q", path, id)
		}
		seen[id] = true
		switch cfgval.AsString(rm["severity"]) {
		case "error", "warning", "ok":
		default:
			add("%s.analyze rule %q severity must be error, warning or ok", path, id)
		}
		switch cfgval.AsString(rm["stream"]) {
		case "", "both", "stdout", "stderr":
		default:
			add("%s.analyze rule %q stream must be stdout, stderr or both", path, id)
		}
		if _, err := regexp.Compile(cfgval.AsString(rm["match"])); err != nil {
			add("%s.analyze rule %q has an invalid regex: %v", path, id, err)
		}
	}
}

func validateSingleShotCheckFields(path, typ string, entry map[string]any, locksDir string, add addFunc) bool {
	if _, known := knownCheckTypes[typ]; !known {
		// A connection-protocol check (mysql, …): the type names a protocol in
		// the conn registry, validated generically below.
		if proto, isProto := conn.Lookup(typ); isProto {
			validateConnFields(path, entry, proto.RequiresUser(), add)
			validateInterfaceFields(path, entry, add)
			if proto.Name() == "dns" {
				if v, present := entry["resolvconf"]; present {
					if _, ok := v.(bool); !ok {
						add("%s.resolvconf must be a boolean", path)
					} else if cfgval.Bool(v) && cfgval.String(entry["host"]) != "" {
						add("%s host and resolvconf are mutually exclusive", path)
					}
				}
			}
			return true
		}
		return false
	}
	validateInterfaceFields(path, entry, add)
	switch typ {
	case "tcp":
		if n, ok := cfgval.Int(entry["port"]); !ok || n < 1 || n > 65535 {
			add("%s.port is required and must be a port in 1..65535 for a tcp check", path)
		}
	case "http":
		validateHTTPFields(path, entry, add)
	case "ports":
		validatePortsFields(path, entry, add)
	case "command":
		if !isStringArray(entry["command"]) {
			add("%s command must be an array, not a shell string", path)
		}
		if v, present := entry["expect_exit"]; present {
			if _, ok := cfgval.Int(v); !ok {
				add("%s expect_exit must be an integer", path)
			}
		}
		validateOutputExpectation(path, "expect_stdout", entry["expect_stdout"], add)
		validateOutputExpectation(path, "expect_stderr", entry["expect_stderr"], add)
		validateAnalyze(path, entry, add)
	case "service":
		if st := cfgval.String(entry["expect"]); st != "" {
			if _, ok := serviceStates[st]; !ok {
				add("%s expect %q is not one of active, inactive, failed, unknown", path, st)
			}
		}
	case "process":
		if st := cfgval.String(entry["state"]); st != "" {
			if _, ok := processStates[st]; !ok {
				add("%s state %q is not one of running, zombie, absent", path, st)
			}
		}
	case "file_exists":
		p := cfgval.String(entry["path"])
		if p == "" {
			add("%s.path is required for a file_exists check", path)
		} else if underDir(p, locksDir) {
			add("%s file_exists must not point under the runtime lock dir %s", path, locksDir)
		}
	case "binary":
		if cfgval.String(entry["path"]) == "" {
			add("%s.path is required for a binary check", path)
		}
	case "pidfile":
		if cfgval.String(entry["path"]) == "" {
			add("%s.path is required for a pidfile check", path)
		}
	case "libraries":
		if cfgval.String(entry["binary"]) == "" {
			add("%s.binary is required for a libraries check", path)
		}
	case "metric":
		validateMetric(entry, path, true, add)
	case "count":
		validateCount(entry, path, add)
	case "storage", "disk":
		validateDiskFields(path, entry, add)
	case "autofs":
		validateAutofsFields(path, entry, add)
	case "load":
		validateLoadFields(path, entry, add)
	case "hdparm":
		validateHdparmFields(path, entry, add)
	case "sensors":
		if validatePresentThresholds(path, entry, checks.SensorPredFields, add) == 0 {
			add("%s requires at least one of temp/fan/voltage {op, value}", path)
		}
	case "smart":
		validateSmartFields(path, entry, add)
	case "raid":
		validatePresentThresholds(path, entry, checks.RaidPredFields, add)
	case "edac":
		validatePresentThresholds(path, entry, checks.EdacPredFields, add)
	case "config":
		_, hasCmd := entry["command"]
		_, hasPath := entry["path"]
		if !hasCmd && !hasPath {
			add("%s requires a command and/or path", path)
		}
		if hasCmd && !isStringArray(entry["command"]) {
			add("%s command must be an array, not a shell string", path)
		}
	case "fds":
		validateThresholdPreds(path, entry, checks.FdsPredFields, add)
	case "memory":
		validateThresholdPreds(path, entry, checks.MemoryPredFields, add)
	case "pressure":
		validatePressureFields(path, entry, add)
	case "pids":
		validateThresholdPreds(path, entry, checks.PidsPredFields, add)
	case "diskio":
		validateDiskIOFields(path, entry, add)
	case "conntrack":
		validateThresholdPreds(path, entry, checks.ConntrackPredFields, add)
	case "firewall_rules":
		validateFirewallRulesFields(path, entry, add)
	case "net":
		if cfgval.String(entry["interface"]) == "" {
			add("%s.interface is required for a net check", path)
		}
		validateNetMetricCondition(path, cfgval.String(entry["metric"]), entry, add)
	case "icmp":
		if cfgval.String(entry["host"]) == "" {
			add("%s.host is required for an icmp check", path)
		}
		if v, present := entry["count"]; present {
			if n, ok := cfgval.Int(v); !ok || n <= 0 {
				add("%s.count must be a positive integer", path)
			}
		}
		validateICMPMetricCondition(path, cfgval.String(entry["metric"]), entry, add)
	case "swap":
		validateSwapMetricCondition(path, cfgval.String(entry["metric"]), entry, add)
	case "route":
		if f := cfgval.String(entry["family"]); f != "" && f != "ipv4" && f != "ipv6" {
			add("%s.family must be ipv4 or ipv6", path)
		}
		if v, present := entry["interface"]; present {
			if _, ok := v.(string); !ok {
				add("%s.interface must be a single interface name for a route check", path)
			}
		}
	case "entropy":
		validateThresholdPreds(path, entry, checks.EntropyPredFields, add)
	case "zombies":
		validateThresholdPreds(path, entry, checks.ZombiePredFields, add)
	case "oom":
		validateOomFields(path, entry, add)
	case "cert":
		validateCertFields(path, entry, add)
	case "sqlite", "sqlite3":
		if cfgval.String(entry["path"]) == "" {
			add("%s.path is required for a sqlite check", path)
		}
	case "sql":
		validateSQLFields(path, entry, add)
	case "mongodb-query":
		validateMongoFields(path, entry, add)
	case "influxdb-query":
		validateInfluxFields(path, entry, add)
	case "size":
		validateSizeFields(path, entry, add)
	case "websocket", "ws":
		validateWebsocketFields(path, entry, add)
	}
	return true
}

func validateFirewallRulesFields(prefix string, fields map[string]any, add addFunc) {
	backend := cfgval.String(fields["backend"])
	if backend == "nft" {
		backend = "nftables"
	}
	switch backend {
	case "", "auto", "nftables", "iptables":
	default:
		add("%s.backend must be auto, nftables or iptables", prefix)
	}
	if v, present := fields["min_rules"]; present {
		n, ok := cfgval.Int(v)
		if !ok || n < 1 {
			add("%s.min_rules must be a positive integer", prefix)
		}
	}
}

// validateWebsocketFields validates a websocket check: a required url with a
// ws/wss/http/https scheme.
func validateWebsocketFields(prefix string, fields map[string]any, add addFunc) {
	raw := cfgval.String(fields["url"])
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
	if cfgval.String(fields["path"]) != "" {
		add("%s: path and count are mutually exclusive", prefix)
	}
	op := cfgval.String(count["op"])
	if !cfgval.IsCompareOp(op) {
		add("%s.count.op %q is not one of >, >=, <, <=, ==, !=", prefix, op)
	}
	if !isNumeric(cfgval.String(count["value"])) {
		add("%s.count.value must be numeric", prefix)
	}
}

// validateSizeFields validates a size (growth) check: a required path, a
// positive parseable grow_by byte size and a positive within duration.
func validateSizeFields(prefix string, fields map[string]any, add addFunc) {
	if cfgval.String(fields["path"]) == "" {
		add("%s.path is required for a size check", prefix)
	}
	gb := cfgval.String(fields["grow_by"])
	if gb == "" {
		add("%s.grow_by is required for a size check (e.g. 1G)", prefix)
	} else if n, ok := cfgval.ByteSize(gb); !ok || n == 0 {
		add("%s.grow_by %q must be a positive size with a K/M/G/T suffix (e.g. 1G, 500M)", prefix, gb)
	}
	w := cfgval.String(fields["within"])
	if w == "" {
		add("%s.within is required for a size check (e.g. 1h)", prefix)
	} else if !isPositiveDuration(w) {
		add("%s.within %q must be a valid positive duration", prefix, w)
	}
}

// validateMongoFields validates a mongodb-query check: a valid op and value, a
// coherent query shape (count via collection[+filter] / aggregate via
// collection+pipeline / command), JSON-parseable filter/pipeline/command, and a
// result path where one is needed.
func validateMongoFields(prefix string, fields map[string]any, add addFunc) {
	op := cfgval.String(fields["op"])
	if !cfgval.IsAssertOp(op) {
		add("%s.op %q is not one of ==, !=, >, >=, <, <=, contains, =~", prefix, op)
	}
	if cfgval.String(fields["value"]) == "" {
		add("%s.value is required for a mongodb-query check", prefix)
	}

	collection := cfgval.String(fields["collection"])
	command := cfgval.String(fields["command"])
	pipeline := cfgval.String(fields["pipeline"])
	result := cfgval.String(fields["result"])

	switch {
	case command != "":
		if collection != "" || pipeline != "" {
			add("%s: command cannot be combined with collection/pipeline", prefix)
		}
		if result == "" {
			add("%s.result is required with command", prefix)
		}
		if !isJSONObject(command) {
			add("%s.command must be a JSON object", prefix)
		}
	case collection != "":
		if cfgval.String(fields["database"]) == "" {
			add("%s.database is required for a collection query", prefix)
		}
		if pipeline != "" {
			if result == "" {
				add("%s.result is required with pipeline", prefix)
			}
			if !isJSONArray(pipeline) {
				add("%s.pipeline must be a JSON array", prefix)
			}
		} else if f := cfgval.String(fields["filter"]); f != "" && !isJSONObject(f) {
			add("%s.filter must be a JSON object", prefix)
		}
	default:
		add("%s requires a collection (+filter), a collection+pipeline, or a command", prefix)
	}
}

// validateInterfaceFields validates the optional egress-interface selection
// shared by network checks: `interface` is a string or a list of strings (a
// name/IP/MAC), and `interface_match` is any|all.
func validateInterfaceFields(prefix string, fields map[string]any, add addFunc) {
	if v, ok := fields["interface"]; ok {
		switch t := v.(type) {
		case string:
		case []any:
			for _, e := range t {
				if _, ok := e.(string); !ok {
					add("%s.interface list entries must be strings (name/IP/MAC)", prefix)
					break
				}
			}
		default:
			add("%s.interface must be a string or a list of strings (name/IP/MAC)", prefix)
		}
	}
	if m := cfgval.String(fields["interface_match"]); m != "" && m != "any" && m != "all" {
		add("%s.interface_match %q must be any or all", prefix, m)
	}
}

// validateInfluxFields validates an influxdb-query check: a query, a valid op and
// a value, plus the language-specific target — InfluxQL needs a `database`, Flux
// needs an `org` and `token`.
func validateInfluxFields(prefix string, fields map[string]any, add addFunc) {
	if cfgval.String(fields["query"]) == "" {
		add("%s.query is required for an influxdb-query check", prefix)
	}
	op := cfgval.String(fields["op"])
	if !cfgval.IsAssertOp(op) {
		add("%s.op %q is not one of ==, !=, >, >=, <, <=, contains, =~", prefix, op)
	}
	if cfgval.String(fields["value"]) == "" {
		add("%s.value is required for an influxdb-query check", prefix)
	}
	language := cfgval.String(fields["language"])
	if language == "" {
		language = "influxql"
	}
	switch language {
	case "influxql":
		if cfgval.String(fields["database"]) == "" {
			add("%s.database is required for an influxql query", prefix)
		}
	case "flux":
		if cfgval.String(fields["org"]) == "" {
			add("%s.org is required for a flux query", prefix)
		}
		if cfgval.String(fields["token"]) == "" {
			add("%s.token is required for a flux query", prefix)
		}
	default:
		add("%s.language %q must be influxql or flux", prefix, language)
	}
}

// isJSONObject / isJSONArray report whether s is a syntactically valid JSON
// object / array (extended-JSON for MongoDB is valid JSON syntax).
func isJSONObject(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "{") && json.Valid([]byte(t))
}

func isJSONArray(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "[") && json.Valid([]byte(t))
}

// validateSQLFields validates a sql check: a known engine, a query, a valid op
// and a value. For numeric ops the value must be numeric; for =~ it must be a
// valid regexp. mysql/postgres require a user; sqlite requires a path.
func validateSQLFields(prefix string, fields map[string]any, add addFunc) {
	engine := cfgval.String(fields["engine"])
	if _, ok := sqlEngines[engine]; !ok {
		add("%s.engine must be one of mysql, mariadb, postgres, postgresql, sqlite", prefix)
	}
	if cfgval.String(fields["query"]) == "" {
		add("%s.query is required for a sql check", prefix)
	}
	op := cfgval.String(fields["op"])
	if !cfgval.IsAssertOp(op) {
		add("%s.op %q is not one of ==, !=, >, >=, <, <=, contains, =~", prefix, op)
	}
	value := cfgval.String(fields["value"])
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
		if cfgval.String(fields["path"]) == "" {
			add("%s.path is required for a sqlite sql check", prefix)
		}
	case "mysql", "mariadb", "postgres", "postgresql":
		if cfgval.String(fields["user"]) == "" {
			add("%s.user is required for a %s sql check", prefix, engine)
		}
	}
}

// validateCertFields validates a cert check at prefix: exactly one of host (a
// live TLS endpoint) or path (a PEM file), optional port (1..65535), optional
// positive expires_in_days, and boolean toggles. New certificate conditions add
// here.
func validateCertFields(prefix string, fields map[string]any, add addFunc) {
	host := cfgval.String(fields["host"])
	path := cfgval.String(fields["path"])
	switch {
	case host == "" && path == "":
		add("%s requires a host or a path", prefix)
	case host != "" && path != "":
		add("%s.host and %s.path are mutually exclusive", prefix, prefix)
	}
	if v, present := fields["port"]; present {
		if n, ok := cfgval.Int(v); !ok || n < 1 || n > 65535 {
			add("%s.port must be an integer in 1..65535", prefix)
		}
	}
	if v, present := fields["server_name"]; present {
		if _, ok := v.(string); !ok {
			add("%s.server_name must be a string (SNI + hostname to verify)", prefix)
		}
	}
	// A PEM file has no endpoint: port and server_name only make sense with host.
	if host == "" && path != "" {
		for _, key := range []string{"port", "server_name"} {
			if _, present := fields[key]; present {
				add("%s.%s does not apply to a PEM file path", prefix, key)
			}
		}
	}
	if v, present := fields["expires_in_days"]; present {
		if n, ok := cfgval.Int(v); !ok || n < 1 {
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

// validateDiskIOFields validates a diskio check: a required block device name
// and at least one rate predicate.
func validateDiskIOFields(prefix string, fields map[string]any, add addFunc) {
	if cfgval.String(fields["device"]) == "" {
		add("%s.device is required for a diskio check (e.g. sda, nvme0n1)", prefix)
	}
	validateThresholdPreds(prefix, fields, checks.DiskIOPredFields, add)
}

// validatePressureFields validates a pressure (PSI) check: a required resource
// (cpu, memory or io) and at least one some_*/full_* stall predicate.
func validatePressureFields(prefix string, fields map[string]any, add addFunc) {
	switch cfgval.String(fields["resource"]) {
	case "cpu", "memory", "io":
	default:
		add("%s.resource must be cpu, memory or io for a pressure check", prefix)
	}
	validateThresholdPreds(prefix, fields, checks.PressurePredFields, add)
}

// validateCount checks a count entry: a path, an optional `of` kind, an optional
// boolean `recursive`, and a required numeric threshold — flat op/value at the
// top level, or nested under `count: {op, value}` like the other named
// predicates (use one form, not both).
func validateCount(entry map[string]any, path string, add addFunc) {
	if cfgval.String(entry["path"]) == "" {
		add("%s count check requires a path", path)
	}
	if of := cfgval.String(entry["of"]); of != "" {
		if _, ok := countKinds[of]; !ok {
			add("%s count `of` %q is not one of any, file, dir, symlink", path, of)
		}
	}
	if v, present := entry["recursive"]; present {
		if _, ok := v.(bool); !ok {
			add("%s count recursive must be a boolean", path)
		}
	}
	threshold := entry
	if m, ok := entry["count"].(map[string]any); ok {
		_, hasOp := entry["op"]
		_, hasValue := entry["value"]
		if hasOp || hasValue {
			add("%s count check must not mix a nested count {op, value} with top-level op/value", path)
		}
		threshold = m
	}
	if op := cfgval.String(threshold["op"]); !isValidDiskOp(op) {
		add("%s count check requires a valid op (>=, >, <=, <, ==, !=)", path)
	}
	if !isNumeric(cfgval.String(threshold["value"])) {
		add("%s count check value %q must be numeric", path, cfgval.String(threshold["value"]))
	}
}
