package config

import (
	"encoding/json"
	"maps"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/conn"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

const portSpecRequiredMessage = `is required (e.g. "80,443,1024-4000")`

// validateStorageFields validates a storage check's fields at prefix (the dotted path
// to the fields container, e.g. "watches.storage-root.check" or "checks.root").
// Shared by host watches and service checks. A storage check verifies space/inodes
// and/or whether the path is mounted, so at least one of the two must be present.
func validateStorageFields(prefix string, fields map[string]any, add addFunc) {
	if cfgval.String(fields[checks.CheckKeyPath]) == "" {
		add("%s.path is required for a storage check", prefix)
	}
	preds := validatePresentThresholds(prefix, fields, checks.StoragePredFields, add)
	hasMount := validateMountConditions(prefix, fields, add)
	if preds == 0 && !hasMount {
		add("%s requires a space/inode predicate (used_pct/free_pct/used_bytes/free_bytes/inodes_*) and/or a mount condition (mounted)", prefix)
	}
}

// validateMountConditions validates the optional mount predicate of a storage
// check and reports whether it was present.
func validateMountConditions(prefix string, fields map[string]any, add addFunc) bool {
	active := false
	if v, present := fields[checks.CheckKeyMounted]; present {
		active = true
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, prefix+"."+checks.CheckKeyMounted)
		}
	}
	for _, field := range []string{checks.CheckKeyFSType, checks.CheckKeyDevice, checks.CheckKeyOptions} {
		if _, present := fields[field]; present {
			add("%s.%s is not supported for a storage check; use mounted to verify the mount point", prefix, field)
		}
	}
	return active
}

// validateOpNumeric validates an already-extracted {op, value} threshold map (a
// storage-style comparison op and a numeric value) at the dotted label. It is the
// shared core of every delta/threshold/predicate check.
func validateOpNumeric(label string, m map[string]any, add addFunc) {
	validateCompareOp(label, m, add)
	if !isNumeric(cfgval.String(m[checks.CheckKeyValue])) {
		add("%s value %q must be numeric", label, cfgval.String(m[checks.CheckKeyValue]))
	}
}

func validateOpByteSize(label string, m map[string]any, add addFunc) {
	validateCompareOp(label, m, add)
	if _, ok := cfgval.ByteSize(m[checks.CheckKeyValue]); !ok {
		add("%s value %q must include a size suffix (K, M, G or T; e.g. 10G, 500M)", label, cfgval.String(m[checks.CheckKeyValue]))
	}
}

func validateOpPercent(label string, m map[string]any, add addFunc) {
	validateCompareOp(label, m, add)
	if !isPercentValue(cfgval.String(m[checks.CheckKeyValue])) {
		add("%s value %q must be a percentage in %s (e.g. 90 or 90%%)", label, cfgval.String(m[checks.CheckKeyValue]), cfgval.PercentRange())
	}
}

// validateCompareOp adds an error when the {op} of an already-extracted threshold
// map is not one of the comparison operators the storage-style checks share. It is
// the op-validation prologue every validateOp* helper repeats.
func validateCompareOp(label string, m map[string]any, add addFunc) {
	if op := cfgval.String(m[checks.CheckKeyOp]); !isValidCompareOp(op) {
		add("%s has an invalid op %q", label, op)
	}
}

func isPercentValue(s string) bool {
	_, ok := cfgval.Percent(s)
	return ok
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
		case strings.HasSuffix(field, checks.LevelFieldSuffixBytes):
			validateOpByteSize(prefix+"."+field, m, add)
		case strings.HasSuffix(field, checks.LevelFieldSuffixPct):
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
	delta, present := fields[checks.CheckKeyDelta]
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
	if v, present := entry[keyRequires]; present {
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
	if v, present := entry[checks.CheckKeySkipWhenChanged]; present {
		if _, ok := gateStrings(v); !ok {
			add("%s.skip_when_changed must be a file path or a list of file paths", path)
		}
	}
}

// gateStrings accepts a scalar string or a list of strings, returning the values
// and whether the shape is valid.
func gateStrings(v any) ([]string, bool) {
	list, err := cfgval.StrictStringList(v)
	return list, err == nil
}

// validateHTTPFields validates an http check at prefix: a required url, and the
// optional request (method/headers/body/json) and response-assertion fields
// (expect_body/expect_json) shapes.
func validateHTTPFields(prefix string, fields map[string]any, add addFunc) {
	if cfgval.String(fields[checks.CheckKeyURL]) == "" {
		add("%s.url is required for an http check", prefix)
	}
	if v, present := fields[checks.CheckKeyMethod]; present {
		if _, warn := checks.ParseHTTPMethod(v); warn != "" {
			add("%s.%s", prefix, warn)
		}
	}
	validateHTTPTransport(prefix, fields, add)
	validateHTTPRequest(prefix, fields, add)
	validateHTTPExpectations(prefix, fields, add)
}

func validateHTTPTransport(prefix string, fields map[string]any, add addFunc) {
	if v, present := fields[checks.CheckKeyHTTP3]; present {
		if h3, ok := v.(bool); !ok {
			add(validationBooleanFormat, prefix+"."+checks.CheckKeyHTTP3)
		} else if h3 {
			// HTTP/3 runs over QUIC (TLS-only) and cannot use an HTTP proxy.
			if u := cfgval.String(fields[checks.CheckKeyURL]); u != "" {
				if parsed, err := url.Parse(u); err != nil || parsed.Scheme != checks.URLSchemeHTTPS {
					add("%s.http3 requires an https url", prefix)
				}
			}
			if cfgval.String(fields[checks.CheckKeyProxy]) != "" {
				add("%s.http3 and proxy are mutually exclusive", prefix)
			}
			if len(cfgval.StringList(fields[checks.CheckKeyInterface])) > 0 {
				add("%s.http3 and interface are mutually exclusive", prefix)
			}
		}
	}
	if v, present := fields[checks.CheckKeyFollowRedirects]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, prefix+"."+checks.CheckKeyFollowRedirects)
		}
	}
	if p := cfgval.String(fields[checks.CheckKeyProxy]); p != "" {
		u, err := url.Parse(p)
		if err != nil || u.Host == "" {
			add("%s.proxy %q is not a valid URL", prefix, p)
		} else if !checks.IsHTTPProxyScheme(u.Scheme) {
			add("%s.proxy scheme must be %s", prefix, checks.HTTPProxySchemeList)
		}
	}
}

func validateHTTPRequest(prefix string, fields map[string]any, add addFunc) {
	if v, present := fields[checks.CheckKeyBody]; present {
		if _, ok := v.(string); !ok {
			add("%s.body must be a string", prefix)
		}
	}
	if j, hasJSON := fields[checks.CheckKeyJSON]; hasJSON && j != nil {
		if _, hasBody := fields[checks.CheckKeyBody]; hasBody {
			add("%s.body and json are mutually exclusive", prefix)
		}
	}
	if v, present := fields[checks.CheckKeyHeaders]; present {
		if _, ok := v.(map[string]any); !ok {
			add("%s.headers must be a mapping", prefix)
		}
	}
}

func validateHTTPExpectations(prefix string, fields map[string]any, add addFunc) {
	if v, present := fields[checks.CheckKeyExpectBody]; present {
		if m, ok := v.(map[string]any); ok {
			validateOpValue(prefix, checks.CheckKeyExpectBody, m, add)
		} else {
			add("%s.expect_body must be an {op, value} mapping", prefix)
		}
	}
	if m, ok := fields[checks.CheckKeyExpectStatus].(map[string]any); ok {
		validateOpValue(prefix, checks.CheckKeyExpectStatus, m, add)
	}
	if v, present := fields[checks.CheckKeyExpectLatency]; present {
		if m, ok := v.(map[string]any); ok {
			validateOpValue(prefix, checks.CheckKeyExpectLatency, m, add)
		} else {
			add("%s.expect_latency must be an {op, value} mapping", prefix)
		}
	}
	value, present := fields[checks.CheckKeyExpectJSON]
	validateHTTPJSONExpectations(prefix, value, present, add)
}

func validateHTTPJSONExpectations(prefix string, value any, present bool, add addFunc) {
	if !present {
		return
	}
	m, ok := value.(map[string]any)
	if !ok {
		add("%s.expect_json must be a mapping", prefix)
		return
	}
	for _, path := range slices.Sorted(maps.Keys(m)) {
		cond, ok := m[path].(map[string]any)
		if !ok {
			continue
		}
		op := cfgval.String(cond[checks.CheckKeyOp])
		if op == "" {
			op = cfgval.CompareOpEqual
		}
		if !cfgval.IsAssertOp(op) {
			add("%s.expect_json.%s op %q is not one of %s", prefix, path, op, cfgval.AssertOpSummary)
			continue
		}
		if err := checks.ValidateAssertionValue(prefix+"."+checks.CheckKeyExpectJSON+"."+path, op, cfgval.String(cond[checks.CheckKeyValue])); err != nil {
			add("%s", err)
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

func validateVersionMatcherField(prefix string, v any, add addFunc) {
	if v == nil {
		return
	}
	if _, warn := checks.ParseVersionMatcher(v); warn != "" {
		add("%s.version_match %s", prefix, warn)
	}
}

func validateCommandFields(path string, entry map[string]any, validateAnalyzeFields bool, add addFunc) {
	if !cfgval.IsNonEmptyStringArray(entry[checks.CheckKeyCommand]) {
		add("%s command must be an array, not a shell string", path)
	}
	validateCommandUser(path, entry, add)
	validateCommandExpectations(path, entry, add)
	if v, present := entry[checks.CheckKeyOnChange]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, path+"."+checks.CheckKeyOnChange)
		}
	}
	validateVersionMatcherField(path, entry[checks.CheckKeyVersionMatch], add)
	if validateAnalyzeFields {
		validateAnalyze(path, entry, add)
	}
	validateCommandExport(path, entry, add)
}

func validateCommandExpectations(path string, entry map[string]any, add addFunc) {
	if v, present := entry[checks.CheckKeyExpectExit]; present {
		if !isExpectExit(v) {
			add("%s expect_exit must be an integer or a non-empty list of integers", path)
		}
	}
	validateOutputExpectation(path, checks.CheckKeyExpectStdout, entry[checks.CheckKeyExpectStdout], add)
	validateOutputExpectation(path, checks.CheckKeyExpectStderr, entry[checks.CheckKeyExpectStderr], add)
}

func isExpectExit(raw any) bool {
	_, ok := cfgval.IntList(raw)
	return ok
}

// validateOpValue validates an {op, value} comparison mapping (shared by the
// http response comparisons): op must be a known comparison operator, and value
// must be numeric for ordering ops and a valid regexp for =~.
func validateOpValue(prefix, label string, m map[string]any, add addFunc) {
	op := cfgval.String(m[checks.CheckKeyOp])
	if !cfgval.IsAssertOp(op) {
		add("%s.%s op %q is not one of %s", prefix, label, op, cfgval.AssertOpSummary)
		return
	}
	value := cfgval.String(m[checks.CheckKeyValue])
	if err := checks.ValidateAssertionValue(prefix+"."+label, op, value); err != nil {
		add("%s", err)
	}
}

// validatePortsFields validates a ports check at prefix: a parseable `ports` spec
// (list + ranges) and the enumerated expect/match values.
func validatePortsFields(prefix string, fields map[string]any, add addFunc) {
	if err := validatePortSpec(cfgval.String(fields[checks.CheckKeyPorts])); err != "" {
		add("%s.ports %s", prefix, err)
	}
	if v := cfgval.String(fields[checks.CheckKeyExpect]); v != "" && v != checks.PortStateOpen && v != checks.PortStateClosed && v != checks.PortExpectAny {
		add("%s.expect must be %s", prefix, checks.PortExpectSummary)
	}
	if v := cfgval.String(fields[checks.CheckKeyMatch]); v != "" && v != checks.PortMatchAll && v != checks.PortMatchAny && v != checks.PortMatchNone {
		add("%s.match must be %s", prefix, checks.PortMatchSummary)
	}
	if v, present := fields[checks.CheckKeyOnChange]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, prefix+"."+checks.CheckKeyOnChange)
		}
	}
	if v, present := fields[checks.CheckKeyConnectTimeout]; present && !isPositiveDuration(cfgval.String(v)) {
		add("%s.connect_timeout must be a valid positive duration", prefix)
	}
}

// validateClockFields validates a clock drift check: explicit NTP servers, a
// positive max_offset threshold, and optional quality ceilings.
func validateClockFields(prefix string, fields map[string]any, add addFunc) {
	servers, err := cfgval.StrictStringList(fields[checks.CheckKeyServers])
	if err != nil || len(servers) == 0 {
		add("%s.servers must be a non-empty string or list of strings", prefix)
	}
	if !isPositiveDuration(cfgval.String(fields[checks.CheckKeyMaxOffset])) {
		add("%s.max_offset must be a valid positive duration", prefix)
	}
	if raw, present := fields[checks.CheckKeyMaxStratum]; present {
		n, ok := cfgval.Int(raw)
		if !ok || n < checks.ClockMinStratum || n > checks.ClockMaxStratum {
			add("%s.max_stratum must be an integer in %d..%d", prefix, checks.ClockMinStratum, checks.ClockMaxStratum)
		}
	}
	if raw, present := fields[checks.CheckKeyMaxRootDispersion]; present && !isPositiveDuration(cfgval.String(raw)) {
		add("%s.max_root_dispersion must be a valid positive duration", prefix)
	}
	if v, present := fields[checks.CheckKeyPort]; present {
		if n, ok := cfgval.Int(v); !ok || !validTCPPort(n) {
			add("%s.port %q must be an integer in %s", prefix, cfgval.String(v), cfgval.TCPPortRange())
		}
	}
}

// validatePortSpec returns "" when spec is a valid comma-separated list of ports
// and inclusive ranges (e.g. "80,443,1024-4000"), else a short reason.
func validatePortSpec(spec string) string {
	if strings.TrimSpace(spec) == "" {
		return portSpecRequiredMessage
	}
	if _, err := checks.ParsePortSpec(spec); err != nil {
		msg := strings.TrimPrefix(err.Error(), "port ")
		switch {
		case strings.HasPrefix(msg, "invalid port range "):
			return "has an invalid range " + strings.TrimPrefix(msg, "invalid port range ")
		case strings.HasPrefix(msg, "invalid port "):
			return "has an invalid port " + strings.TrimPrefix(msg, "invalid port ")
		}
		if msg == "no ports specified" {
			return portSpecRequiredMessage
		}
		return msg
	}
	return ""
}

// validateLoadFields validates a load check at prefix: an optional boolean
// per_cpu and at least one load1/load5/load15 threshold.
func validateLoadFields(prefix string, fields map[string]any, add addFunc) {
	if v, present := fields[checks.CheckKeyPerCPU]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, prefix+"."+checks.CheckKeyPerCPU)
		}
	}
	validateThresholdPreds(prefix, fields, checks.LoadPredFields, add)
}

// validateHdparmFields validates an hdparm check: a required device and at least
// one of the read/cached {op, value} throughput predicates.
func validateHdparmFields(prefix string, fields map[string]any, add addFunc) {
	if cfgval.String(fields[checks.CheckKeyDevice]) == "" {
		add("%s.device is required for an hdparm check", prefix)
	}
	if validatePresentThresholds(prefix, fields, checks.HdparmPredFields, add) == 0 {
		add("%s requires at least one of %s {op, value}", prefix, strings.Join(checks.HdparmPredFields, "/"))
	}
}

// validateSmartFields validates a smart check: a required device and any of the
// optional {op, value} attribute predicates (without one, it alerts on a failed
// SMART health verdict).
func validateSmartFields(prefix string, fields map[string]any, add addFunc) {
	if cfgval.String(fields[checks.CheckKeyDevice]) == "" {
		add("%s.device is required for a smart check", prefix)
	}
	validatePresentThresholds(prefix, fields, checks.SmartPredFields, add)
}

// isValidCompareOp reports whether op is one of the comparison operators shared by
// every {op, value} threshold — the single set in cfgval, shared with the
// runtime builders so the two grammars cannot drift apart.
func isValidCompareOp(op string) bool {
	return cfgval.IsCompareOp(op)
}

func isNumeric(s string) bool {
	_, ok := cfgval.Float(s)
	return ok
}

// validateConnFields validates a connection-protocol check (mysql, …): a user
// is required (password is optional and may come from the environment), the
// port must be numeric when present, and tls must be a boolean or one of the
// known string modes.
func validateConnFields(prefix string, fields map[string]any, requireUser bool, add addFunc) {
	if requireUser && cfgval.String(fields[checks.CheckKeyUser]) == "" {
		add("%s.user is required for a connection check", prefix)
	}
	validateConnPort(prefix, fields, add)
	validateConnTLS(prefix, fields, add)
	validateConnExpectations(prefix, fields, add)
	validateConnChangeFlags(prefix, fields, add)
}

func validateConnPort(prefix string, fields map[string]any, add addFunc) {
	// The same TCP port range walkScalars enforces on resolved services, so a
	// connection check behaves identically as a host watch.
	if v, present := fields[checks.CheckKeyPort]; present {
		if n, ok := cfgval.Int(v); !ok || !validTCPPort(n) {
			add("%s.port %q must be an integer in %s", prefix, cfgval.String(v), cfgval.TCPPortRange())
		}
	}
}

func validateConnTLS(prefix string, fields map[string]any, add addFunc) {
	if v, present := fields[checks.CheckKeyTLS]; present {
		switch t := v.(type) {
		case bool:
			// fine
		case string:
			if !conn.ValidTLSValue(t) {
				add("%s.tls %q must be %s", prefix, t, conn.TLSValueSummary)
			}
		default:
			add("%s.tls must be a boolean or a string (%s)", prefix, conn.TLSScalarSummary)
		}
	}
}

func validateConnExpectations(prefix string, fields map[string]any, add addFunc) {
	// expect: optional response assertions (field -> value | {op, value}),
	// compared against the probe's version / Extra fields.
	if v, present := fields[checks.CheckKeyExpect]; present {
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
	if v, present := fields[checks.CheckKeyExpectLatency]; present {
		if m, ok := v.(map[string]any); ok {
			validateOpValue(prefix, checks.CheckKeyExpectLatency, m, add)
		} else {
			add("%s.expect_latency must be an {op, value} mapping", prefix)
		}
	}
}

func validateConnChangeFlags(prefix string, fields map[string]any, add addFunc) {
	for _, key := range []string{checks.CheckKeyOnChange, checks.CheckKeyOnVersionChange} {
		if v, present := fields[key]; present {
			if _, ok := v.(bool); !ok {
				add(validationBooleanFormat, prefix+"."+key)
			}
		}
	}
}

var countKinds = set(checks.CountKindAny, checks.CountKindFile, checks.CountKindDir, checks.CountKindSymlink)
var sqlEngines = set(
	checks.SQLEngineMySQL,
	checks.SQLEngineMariaDB,
	checks.SQLEnginePostgres,
	checks.SQLEnginePostgreSQL,
	checks.SQLEngineSQLite,
	checks.SQLEngineSQLite3,
)

// validateCheckSection validates a checks/preflight section: known types,
// optional/verify booleans, command array form, valid service/process states,
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
			add(validationMappingFormat, path)
			continue
		}
		if v, present := entry[checks.CheckKeyOptional]; present {
			if _, isBool := v.(bool); !isBool {
				add(validationBooleanFormat, path+"."+checks.CheckKeyOptional)
			}
		}
		validateCheckSummary(path, entry, add)
		// A per-check interval runs the check every N cycles (N rounded from
		// interval/resolution). It must be a positive duration; the daemon warns at
		// startup if it is below the resolution or not an exact multiple.
		if v, present := entry[keyInterval]; present && !isPositiveDuration(cfgval.String(v)) {
			add("%s.interval %q must be a valid positive duration", path, cfgval.String(v))
		}
		if v, present := entry[checks.CheckKeyTimeout]; present && !isPositiveDuration(cfgval.String(v)) {
			add("%s.timeout %q must be a valid positive duration", path, cfgval.String(v))
		}
		validateCheckGate(path, name, entry, entries, add)
		typ := cfgval.String(entry[checks.CheckKeyType])
		if typ == "" {
			add("%s has no type", path)
			continue
		}
		// verify: true marks a check as a post-operation start-verification probe.
		// Only health checks (OK == the service is up) can confirm a start; a
		// condition check's OK means a threshold fired, which is not verification.
		if v, present := entry[checks.CheckKeyVerify]; present {
			if b, ok := v.(bool); !ok {
				add(validationBooleanFormat, path+"."+checks.CheckKeyVerify)
			} else if b && !checks.IsHealthType(typ) {
				add("%s.verify is only valid on a health check (tcp/http/service/command/cert/…); %q is a condition check whose OK does not confirm a successful start", path, typ)
			}
		}
		if !validateSingleShotCheckFields(path, typ, entry, locksDir, add) {
			add("%s has unknown type %q", path, typ)
			continue
		}
	}
}

func validateCheckSummary(path string, entry map[string]any, add addFunc) {
	if value, present := entry[checks.CheckKeySummary]; present {
		if _, ok := value.(string); !ok {
			add("%s.%s must be a string", path, checks.CheckKeySummary)
		}
	}
}

// validateAnalyze statically validates a command check's resolved `analyze`
// block. It runs after expandAnalyze, so `use`/`silence` are gone and only the
// flat `rules` list remains (unknown-set/silence errors are raised during
// resolution). It checks each rule's id (present, unique), severity
// (error|warning|ok), stream (stdout|stderr|both or empty), and that `match`
// is a non-empty regular expression.
func validateAnalyze(path string, entry map[string]any, add addFunc) {
	analyze, ok := entry[checks.CheckKeyAnalyze].(map[string]any)
	if !ok {
		if _, present := entry[checks.CheckKeyAnalyze]; present {
			add(validationAnalyzeMappingFormat, path)
		}
		return
	}
	rules, ok := analyze[checks.CheckKeyRules].([]any)
	if !ok {
		if _, present := analyze[checks.CheckKeyRules]; present {
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
		id := cfgval.AsString(rm[checks.CheckKeyID])
		if id == "" {
			add("%s.analyze rule %d is missing an id", path, i)
		} else if seen[id] {
			add("%s.analyze has a duplicate rule id %q", path, id)
		}
		seen[id] = true
		switch cfgval.AsString(rm[checks.CheckKeySeverity]) {
		case checks.AnalyzeSeverityError, checks.AnalyzeSeverityWarning, checks.AnalyzeSeverityOK:
		default:
			add("%s.analyze rule %q severity must be %s", path, id, checks.AnalyzeSeveritySummary)
		}
		switch cfgval.AsString(rm[checks.CheckKeyStream]) {
		case "", checks.AnalyzeStreamBoth, checks.AnalyzeStreamStdout, checks.AnalyzeStreamStderr:
		default:
			add("%s.analyze rule %q stream must be %s", path, id, checks.AnalyzeStreamSummary)
		}
		match := cfgval.AsString(rm[checks.CheckKeyMatch])
		if match == "" {
			add("%s.analyze rule %q is missing a match", path, id)
			continue
		}
		if _, err := regexp.Compile(match); err != nil {
			add("%s.analyze rule %q has an invalid regex: %v", path, id, err)
		}
	}
}

func validateCommandExport(path string, entry map[string]any, add addFunc) {
	raw, present := entry[checks.CheckKeyExport]
	if !present {
		return
	}
	exports, ok := raw.(map[string]any)
	if !ok {
		add("%s.export must be a mapping of variable name -> export rule", path)
		return
	}
	for _, name := range slices.Sorted(maps.Keys(exports)) {
		if !validVariableName(name) {
			add("%s.export variable %q must be a simple variable name", path, name)
		}
		switch spec := exports[name].(type) {
		case map[string]any:
			if from := cfgval.String(spec[checks.CheckKeyFrom]); from != "" && from != checks.AnalyzeStreamStdout && from != checks.AnalyzeStreamStderr {
				add("%s.export.%s.from must be %s", path, name, checks.AnalyzeExportStreamSummary)
			}
			if v, present := spec[checks.CheckKeyTrim]; present {
				if _, ok := v.(bool); !ok {
					add(validationBooleanFormat, path+"."+checks.CheckKeyExport+"."+name+"."+checks.CheckKeyTrim)
				}
			}
			if rawRegex, present := spec[checks.CheckKeyRegex]; present {
				pattern := cfgval.String(rawRegex)
				if pattern == "" {
					add("%s.export.%s.regex must be non-empty", path, name)
				} else if _, err := regexp.Compile(pattern); err != nil {
					add("%s.export.%s.regex is invalid: %v", path, name, err)
				}
			}
		default:
			add("%s.export.%s must be a mapping", path, name)
		}
	}
}

func validVariableName(name string) bool {
	if name == "" || strings.Contains(name, ".") {
		return false
	}
	for _, r := range name {
		if r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
			continue
		}
		return false
	}
	return true
}

type singleShotCheckValidator func(path string, entry map[string]any, locksDir string, add addFunc)

var singleShotCheckValidators = map[string]singleShotCheckValidator{
	checks.CheckTypeTCP:           validateTCPCheck,
	checks.CheckTypeHTTP:          singleShotNoLock(validateHTTPFields),
	checks.CheckTypePorts:         singleShotNoLock(validatePortsFields),
	checks.CheckTypeCommand:       validateSingleShotCommand,
	checks.CheckTypeClock:         singleShotNoLock(validateClockFields),
	checks.CheckTypeService:       validateServiceCheck,
	checks.CheckTypeProcess:       validateProcessCheck,
	checks.CheckTypeFileExists:    validateFileExistsCheck,
	checks.CheckTypeFile:          validateSingleShotFileCheck,
	checks.CheckTypeLockfile:      validateLockfileCheck,
	checks.CheckTypeBinary:        validateBinaryCheck,
	checks.CheckTypePidfile:       validatePidfileCheck,
	checks.CheckTypeSocket:        validateSocketCheck,
	checks.CheckTypeLibraries:     validateLibrariesCheck,
	checks.CheckTypeMetric:        validateSingleShotMetric,
	checks.CheckTypeCount:         validateSingleShotCount,
	checks.CheckTypeStorage:       singleShotNoLock(validateStorageFields),
	checks.CheckTypeAutofs:        singleShotNoLock(validateAutofsFields),
	checks.CheckTypeLoad:          singleShotNoLock(validateLoadFields),
	checks.CheckTypeUsers:         singleShotThreshold(checks.UsersPredFields),
	checks.CheckTypeProcessCount:  validateProcessCountCheck,
	checks.CheckTypeHdparm:        singleShotNoLock(validateHdparmFields),
	checks.CheckTypeSensors:       validateSensorsCheck,
	checks.CheckTypeSmart:         singleShotNoLock(validateSmartFields),
	checks.CheckTypeRAID:          validateRAIDCheck,
	checks.CheckTypeLVM:           validateLVMCheck,
	checks.CheckTypeEDAC:          singleShotThreshold(checks.EdacPredFields),
	checks.CheckTypeConfig:        validateConfigCheck,
	checks.CheckTypeFDS:           singleShotThreshold(checks.FdsPredFields),
	checks.CheckTypeMemory:        singleShotThreshold(checks.MemoryPredFields),
	checks.CheckTypePressure:      singleShotNoLock(validatePressureFields),
	checks.CheckTypePIDs:          singleShotThreshold(checks.PidsPredFields),
	checks.CheckTypeDiskIO:        singleShotNoLock(validateDiskIOFields),
	checks.CheckTypeConntrack:     singleShotThreshold(checks.ConntrackPredFields),
	checks.CheckTypeFirewallRules: singleShotNoLock(validateFirewallRulesFields),
	checks.CheckTypeNet:           validateNetSingleShotCheck,
	checks.CheckTypeICMP:          validateICMPSingleShotCheck,
	checks.CheckTypeSwap:          validateSwapSingleShotCheck,
	checks.CheckTypeRoute:         validateRouteCheck,
	checks.CheckTypeEntropy:       singleShotThreshold(checks.EntropyPredFields),
	checks.CheckTypeZombies:       singleShotThreshold(checks.ZombiePredFields),
	checks.CheckTypeOOM:           singleShotNoLock(validateOomFields),
	checks.CheckTypeCert:          singleShotNoLock(validateCertFields),
	checks.CheckTypeSQLite:        validateSQLiteCheck,
	checks.CheckTypeSQLite3:       validateSQLiteCheck,
	checks.CheckTypeSQL:           singleShotNoLock(validateSQLFields),
	checks.CheckTypeMongoDBQuery:  singleShotNoLock(validateMongoFields),
	checks.CheckTypeInfluxDBQuery: singleShotNoLock(validateInfluxFields),
	checks.CheckTypeSize:          singleShotNoLock(validateSizeFields),
	checks.CheckTypeWebsocket:     singleShotNoLock(validateWebsocketFields),
}

func singleShotNoLock(validate func(string, map[string]any, addFunc)) singleShotCheckValidator {
	return func(path string, entry map[string]any, _ string, add addFunc) {
		validate(path, entry, add)
	}
}

func singleShotThreshold(fields []string) singleShotCheckValidator {
	return func(path string, entry map[string]any, _ string, add addFunc) {
		validateThresholdPreds(path, entry, fields, add)
	}
}

func validateSingleShotCheckFields(path, typ string, entry map[string]any, locksDir string, add addFunc) bool {
	if !checks.IsSingleShotType(typ) {
		// A connection-protocol check (mysql, …): the type names a protocol in
		// the conn registry, validated generically below.
		if proto, isProto := conn.Lookup(typ); isProto {
			validateConnFields(path, entry, proto.RequiresUser(), add)
			validateInterfaceFields(path, entry, add)
			if proto.Name() == conn.ProtocolNameDNS {
				if v, present := entry[checks.CheckKeyResolvconf]; present {
					if _, ok := v.(bool); !ok {
						add(validationBooleanFormat, path+"."+checks.CheckKeyResolvconf)
					} else if cfgval.Bool(v) && cfgval.String(entry[checks.CheckKeyHost]) != "" {
						add("%s host and resolvconf are mutually exclusive", path)
					}
				}
			}
			return true
		}
		return false
	}
	validateInterfaceFields(path, entry, add)
	if validate := singleShotCheckValidators[typ]; validate != nil {
		validate(path, entry, locksDir, add)
	}
	return true
}

func validateTCPCheck(path string, entry map[string]any, _ string, add addFunc) {
	if n, ok := cfgval.Int(entry[checks.CheckKeyPort]); !ok || !validTCPPort(n) {
		add("%s.port is required and must be a port in %s for a tcp check", path, cfgval.TCPPortRange())
	}
}

func validateSingleShotCommand(path string, entry map[string]any, _ string, add addFunc) {
	validateCommandFields(path, entry, true, add)
}

func validateServiceCheck(path string, entry map[string]any, _ string, add addFunc) {
	state := cfgval.String(entry[checks.CheckKeyExpect])
	if state == "" {
		add("%s.expect is required for a service check", path)
		return
	}
	if _, ok := serviceStates[state]; !ok {
		add("%s expect %q is not one of %s", path, state, servicemgr.StatusSummary)
	}
}

func validateProcessCheck(path string, entry map[string]any, _ string, add addFunc) {
	hasExe := cfgval.String(entry[checks.CheckKeyExe]) != ""
	exeAny, hasExeAnyField := entry[checks.CheckKeyExeAny]
	hasExeAny := cfgval.IsNonEmptyStringList(exeAny)
	if hasExeAnyField && !hasExeAny {
		add("%s.exe_any must be a string or non-empty list of strings", path)
	}
	switch {
	case !hasExe && !hasExeAnyField:
		add("%s.exe or exe_any is required for a process check", path)
	case hasExe && hasExeAny:
		add("%s must define only one of exe or exe_any", path)
	}
	if state := cfgval.String(entry[checks.CheckKeyState]); state != "" {
		if _, ok := processStates[state]; !ok {
			add("%s state %q is not one of %s", path, state, process.StateSummary)
		}
	}
}

func validateFileExistsCheck(path string, entry map[string]any, locksDir string, add addFunc) {
	filePath := cfgval.String(entry[checks.CheckKeyPath])
	if filePath == "" {
		add("%s.path is required for a file_exists check", path)
	} else if underDir(filePath, locksDir) {
		add("%s file_exists must not point under the runtime lock dir %s", path, locksDir)
	}
}

func validateSingleShotFileCheck(path string, entry map[string]any, _ string, add addFunc) {
	validateRequiredStringField(path, entry, checks.CheckKeyPath, checks.CheckTypeFile, add)
	if v, present := entry[checks.CheckKeyNonEmpty]; present {
		if _, ok := v.(bool); !ok {
			add("%s.non_empty must be a boolean", path)
		}
	}
}

func validateLockfileCheck(path string, entry map[string]any, locksDir string, add addFunc) {
	if !cfgval.IsNonEmptyStringList(entry[checks.CheckKeyPath]) {
		add("%s.path is required for a lockfile check", path)
		return
	}
	for _, lockfile := range cfgval.StringList(entry[checks.CheckKeyPath]) {
		if underDir(lockfile, locksDir) {
			add("%s lockfile must not point under the runtime lock dir %s", path, locksDir)
			return
		}
	}
}

func validateBinaryCheck(path string, entry map[string]any, _ string, add addFunc) {
	validateRequiredStringField(path, entry, checks.CheckKeyPath, checks.CheckTypeBinary, add)
}

func validatePidfileCheck(path string, entry map[string]any, _ string, add addFunc) {
	validateRequiredStringListField(path, entry, checks.CheckKeyPath, checks.CheckTypePidfile, add)
}

func validateSocketCheck(path string, entry map[string]any, _ string, add addFunc) {
	validateRequiredStringListField(path, entry, checks.CheckKeyPath, checks.CheckTypeSocket, add)
}

func validateLibrariesCheck(path string, entry map[string]any, _ string, add addFunc) {
	validateRequiredStringField(path, entry, checks.CheckKeyBinary, checks.CheckTypeLibraries, add)
}

func validateRequiredStringField(path string, entry map[string]any, field, checkType string, add addFunc) {
	if cfgval.String(entry[field]) == "" {
		add("%s.%s is required for a %s check", path, field, checkType)
	}
}

func validateRequiredStringListField(path string, entry map[string]any, field, checkType string, add addFunc) {
	if !cfgval.IsNonEmptyStringList(entry[field]) {
		add("%s.%s is required for a %s check", path, field, checkType)
	}
}

func validateSingleShotMetric(path string, entry map[string]any, _ string, add addFunc) {
	validateMetric(entry, path, true, add)
}

func validateSingleShotCount(path string, entry map[string]any, _ string, add addFunc) {
	validateCount(entry, path, add)
}

func validateProcessCountCheck(path string, entry map[string]any, _ string, add addFunc) {
	validateThresholdPreds(path, entry, checks.ProcessCountPredFields, add)
	for _, field := range []string{checks.CheckKeyExe, checks.CheckKeyExeDir} {
		if value := cfgval.String(entry[field]); value != "" && !filepath.IsAbs(value) {
			add("%s.%s must be an absolute path", path, field)
		}
	}
}

func validateSensorsCheck(path string, entry map[string]any, _ string, add addFunc) {
	if validatePresentThresholds(path, entry, checks.SensorPredFields, add) == 0 {
		add("%s requires at least one of %s {op, value}", path, strings.Join(checks.SensorPredFields, "/"))
	}
}

func validateRAIDCheck(path string, entry map[string]any, _ string, add addFunc) {
	validatePresentThresholds(path, entry, checks.RaidPredFields, add)
	if array, present := entry[checks.CheckKeyArray]; present && cfgval.String(array) == "" {
		add("%s.%s must be a non-empty string", path, checks.CheckKeyArray)
	}
}

func validateLVMCheck(path string, entry map[string]any, _ string, add addFunc) {
	validatePresentThresholds(path, entry, checks.LVMPredFields, add)
	volumeGroup := cfgval.String(entry[checks.CheckKeyVolumeGroup])
	logicalVolume := cfgval.String(entry[checks.CheckKeyLogicalVolume])
	if _, present := entry[checks.CheckKeyVolumeGroup]; present && volumeGroup == "" {
		add("%s.%s must be a non-empty string", path, checks.CheckKeyVolumeGroup)
	}
	if _, present := entry[checks.CheckKeyLogicalVolume]; present && logicalVolume == "" {
		add("%s.%s must be a non-empty string", path, checks.CheckKeyLogicalVolume)
	}
	if logicalVolume != "" && volumeGroup == "" {
		add("%s.%s requires %s", path, checks.CheckKeyLogicalVolume, checks.CheckKeyVolumeGroup)
	}
	if _, hasArray := entry[checks.CheckKeyArray]; hasArray {
		if _, hasArrays := entry[checks.DataKeyArrays]; hasArrays {
			add("%s.%s cannot be combined with %s", path, checks.CheckKeyArray, checks.DataKeyArrays)
		}
	}
	if v, present := entry[checks.CheckKeySysfsChanges]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, path+"."+checks.CheckKeySysfsChanges)
		}
	}
}

func validateConfigCheck(path string, entry map[string]any, _ string, add addFunc) {
	_, hasCommand := entry[checks.CheckKeyCommand]
	_, hasPath := entry[checks.CheckKeyPath]
	if !hasCommand && !hasPath {
		add("%s requires a command and/or path", path)
	}
	if hasCommand && !cfgval.IsNonEmptyStringArray(entry[checks.CheckKeyCommand]) {
		add("%s command must be an array, not a shell string", path)
	}
	if hasPath && !cfgval.IsNonEmptyStringList(entry[checks.CheckKeyPath]) {
		add("%s.path must be a string or non-empty list of strings", path)
	}
	if v, present := entry[checks.CheckKeyOnChange]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, path+"."+checks.CheckKeyOnChange)
		}
	}
	if hasCommand {
		validateCommandUser(path, entry, add)
	}
}

func validateNetSingleShotCheck(path string, entry map[string]any, _ string, add addFunc) {
	if cfgval.String(entry[checks.CheckKeyInterface]) == "" {
		add("%s.interface is required for a net check", path)
	}
	validateNetMetricCondition(path, cfgval.String(entry[checks.CheckKeyMetric]), entry, add)
}

func validateICMPSingleShotCheck(path string, entry map[string]any, _ string, add addFunc) {
	if cfgval.String(entry[checks.CheckKeyHost]) == "" {
		add("%s.host is required for an icmp check", path)
	}
	if v, present := entry[checks.CheckKeyCount]; present {
		if n, ok := cfgval.Int(v); !ok || n <= 0 {
			add("%s.count must be a positive integer", path)
		}
	}
	validateICMPMetricCondition(path, cfgval.String(entry[checks.CheckKeyMetric]), entry, add)
}

func validateSwapSingleShotCheck(path string, entry map[string]any, _ string, add addFunc) {
	validateSwapMetricCondition(path, cfgval.String(entry[checks.CheckKeyMetric]), entry, add)
}

func validateRouteCheck(path string, entry map[string]any, _ string, add addFunc) {
	if family := cfgval.String(entry[checks.CheckKeyFamily]); family != "" && family != checks.FamilyIPv4 && family != checks.FamilyIPv6 {
		add("%s.family must be %s", path, checks.RouteFamilySummary)
	}
	if v, present := entry[checks.CheckKeyInterface]; present {
		if _, ok := v.(string); !ok {
			add("%s.interface must be a single interface name for a route check", path)
		}
	}
}

func validateSQLiteCheck(path string, entry map[string]any, _ string, add addFunc) {
	if cfgval.String(entry[checks.CheckKeyPath]) == "" {
		add("%s.path is required for a sqlite check", path)
	}
	if v, present := entry[checks.CheckKeyQuick]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, path+"."+checks.CheckKeyQuick)
		}
	}
}

func validateCommandUser(path string, entry map[string]any, add addFunc) {
	raw, present := entry[checks.CheckKeyUser]
	if !present {
		return
	}
	user, ok := raw.(string)
	if !ok || strings.TrimSpace(user) == "" {
		add("%s user must be a non-empty string", path)
	}
}

func validateFirewallRulesFields(prefix string, fields map[string]any, add addFunc) {
	backend := cfgval.String(fields[checks.CheckKeyBackend])
	if backend == checks.FirewallBackendNftAlias {
		backend = checks.FirewallBackendNftables
	}
	switch backend {
	case "", checks.FirewallBackendAuto, checks.FirewallBackendNftables, checks.FirewallBackendIptables:
	default:
		add("%s.backend must be %s", prefix, checks.FirewallBackendSummary)
	}
	if v, present := fields[checks.CheckKeyMinRules]; present {
		n, ok := cfgval.Int(v)
		if !ok || n < 1 {
			add("%s.min_rules must be a positive integer", prefix)
		}
	}
}

// validateWebsocketFields validates a websocket check: a required url with a
// ws/wss/http/https scheme.
func validateWebsocketFields(prefix string, fields map[string]any, add addFunc) {
	raw := cfgval.String(fields[checks.CheckKeyURL])
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
	case checks.URLSchemeWS, checks.URLSchemeWSS, checks.URLSchemeHTTP, checks.URLSchemeHTTPS:
	default:
		add("%s.url scheme must be %s", prefix, checks.WebsocketURLSchemeSummary)
	}
}

// validateAutofsFields validates an autofs check: an optional count {op, value}
// predicate, mutually exclusive with path.
func validateAutofsFields(prefix string, fields map[string]any, add addFunc) {
	count, hasCount := fields[checks.CheckKeyCount].(map[string]any)
	if !hasCount {
		return
	}
	if cfgval.String(fields[checks.CheckKeyPath]) != "" {
		add("%s: path and count are mutually exclusive", prefix)
	}
	op := cfgval.String(count[checks.CheckKeyOp])
	if !cfgval.IsCompareOp(op) {
		add("%s.count.op %q is not one of %s", prefix, op, cfgval.CompareOpSummary)
	}
	if !isNumeric(cfgval.String(count[checks.CheckKeyValue])) {
		add("%s.count.value must be numeric", prefix)
	}
}

// validateSizeFields validates a size (growth) check: a required path, a
// positive parseable grow_by byte size and a positive within duration.
func validateSizeFields(prefix string, fields map[string]any, add addFunc) {
	if cfgval.String(fields[checks.CheckKeyPath]) == "" {
		add("%s.path is required for a size check", prefix)
	}
	if v, present := fields[checks.CheckKeyIncludeHidden]; present {
		if _, ok := v.(bool); !ok {
			add(validationBooleanFormat, prefix+"."+checks.CheckKeyIncludeHidden)
		}
	}
	gb := cfgval.String(fields[checks.CheckKeyGrowBy])
	if gb == "" {
		add("%s.grow_by is required for a size check (e.g. 1G)", prefix)
	} else if n, ok := cfgval.ByteSize(gb); !ok || n == 0 {
		add("%s.grow_by %q must be a positive size with a K/M/G/T suffix (e.g. 1G, 500M)", prefix, gb)
	}
	w := cfgval.String(fields[checks.CheckKeyWithin])
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
	validateAssertionFields(prefix, fields, "mongodb-query", add)

	collection := cfgval.String(fields[checks.CheckKeyCollection])
	command := cfgval.String(fields[checks.CheckKeyCommand])
	pipeline := cfgval.String(fields[checks.CheckKeyPipeline])
	result := cfgval.String(fields[checks.CheckKeyResult])

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
		if cfgval.String(fields[checks.CheckKeyDatabase]) == "" {
			add("%s.database is required for a collection query", prefix)
		}
		if pipeline != "" {
			if result == "" {
				add("%s.result is required with pipeline", prefix)
			}
			if !isJSONArray(pipeline) {
				add("%s.pipeline must be a JSON array", prefix)
			}
		} else if f := cfgval.String(fields[checks.CheckKeyFilter]); f != "" && !isJSONObject(f) {
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
	if v, ok := fields[checks.CheckKeyInterface]; ok {
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
	if m := cfgval.String(fields[checks.CheckKeyInterfaceMatch]); m != "" && m != checks.InterfaceMatchAny && m != checks.InterfaceMatchAll {
		add("%s.interface_match %q must be %s", prefix, m, checks.InterfaceMatchSummary)
	}
}

// validateInfluxFields validates an influxdb-query check: a query, a valid op and
// a value, plus the language-specific target — InfluxQL needs a `database`, Flux
// needs an `org` and `token`.
func validateInfluxFields(prefix string, fields map[string]any, add addFunc) {
	if cfgval.String(fields[checks.CheckKeyQuery]) == "" {
		add("%s.query is required for an influxdb-query check", prefix)
	}
	validateAssertionFields(prefix, fields, "influxdb-query", add)
	language := cfgval.String(fields[checks.CheckKeyLanguage])
	if language == "" {
		language = checks.InfluxLanguageInfluxQL
	}
	switch language {
	case checks.InfluxLanguageInfluxQL:
		if cfgval.String(fields[checks.CheckKeyDatabase]) == "" {
			add("%s.database is required for an influxql query", prefix)
		}
	case checks.InfluxLanguageFlux:
		if cfgval.String(fields[checks.CheckKeyOrg]) == "" {
			add("%s.org is required for a flux query", prefix)
		}
		if cfgval.String(fields[checks.CheckKeyToken]) == "" {
			add("%s.token is required for a flux query", prefix)
		}
	default:
		add("%s.language %q must be %s", prefix, language, checks.InfluxLanguageSummary)
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
// and value, plus engine-specific connection requirements.
func validateSQLFields(prefix string, fields map[string]any, add addFunc) {
	engine := cfgval.String(fields[checks.CheckKeyEngine])
	if _, ok := sqlEngines[engine]; !ok {
		add("%s.engine must be one of %s", prefix, checks.SQLEngineSummary)
	}
	if cfgval.String(fields[checks.CheckKeyQuery]) == "" {
		add("%s.query is required for a sql check", prefix)
	}
	validateAssertionFields(prefix, fields, "sql", add)
	switch engine {
	case checks.SQLEngineSQLite, checks.SQLEngineSQLite3:
		if cfgval.String(fields[checks.CheckKeyPath]) == "" {
			add("%s.path is required for a sqlite sql check", prefix)
		}
	case checks.SQLEngineMySQL, checks.SQLEngineMariaDB, checks.SQLEnginePostgres, checks.SQLEnginePostgreSQL:
		if cfgval.String(fields[checks.CheckKeyUser]) == "" {
			add("%s.user is required for a %s sql check", prefix, engine)
		}
	}
}

func validateAssertionFields(prefix string, fields map[string]any, checkType string, add addFunc) {
	op := cfgval.String(fields[checks.CheckKeyOp])
	if !cfgval.IsAssertOp(op) {
		add("%s.op %q is not one of %s", prefix, op, cfgval.AssertOpSummary)
	}
	value := cfgval.String(fields[checks.CheckKeyValue])
	if value == "" {
		add("%s.value is required for a %s check", prefix, checkType)
	} else if cfgval.IsAssertOp(op) {
		if err := checks.ValidateAssertionValue(prefix, op, value); err != nil {
			add("%s", err)
		}
	}
}

// validateCertFields validates a cert check at prefix: exactly one of host (a
// live TLS endpoint) or path (a PEM file), optional valid TCP port, optional
// positive expires_in_days, and boolean toggles. New certificate conditions add
// here.
func validateCertFields(prefix string, fields map[string]any, add addFunc) {
	host := cfgval.String(fields[checks.CheckKeyHost])
	path := cfgval.String(fields[checks.CheckKeyPath])
	switch {
	case host == "" && path == "":
		add("%s requires a host or a path", prefix)
	case host != "" && path != "":
		add("%s.host and %s.path are mutually exclusive", prefix, prefix)
	}
	if v, present := fields[checks.CheckKeyPort]; present {
		if n, ok := cfgval.Int(v); !ok || !validTCPPort(n) {
			add(validationTCPPortRangeFormat, prefix+"."+checks.CheckKeyPort, cfgval.TCPPortRange())
		}
	}
	if v, present := fields[checks.CheckKeyServerName]; present {
		if _, ok := v.(string); !ok {
			add("%s.server_name must be a string (SNI + hostname to verify)", prefix)
		}
	}
	// A PEM file has no endpoint: port and server_name only make sense with host.
	if host == "" && path != "" {
		for _, key := range []string{checks.CheckKeyPort, checks.CheckKeyServerName} {
			if _, present := fields[key]; present {
				add("%s.%s does not apply to a PEM file path", prefix, key)
			}
		}
	}
	if v, present := fields[checks.CheckKeyExpiresInDays]; present {
		if n, ok := cfgval.Int(v); !ok || n < 1 {
			add("%s.expires_in_days must be a positive integer", prefix)
		}
	}
	for _, key := range []string{checks.CheckKeyOnAlgorithmChange, checks.CheckKeyOnIssuerChange, checks.CheckKeyOnChange, checks.CheckKeyCertVerify} {
		if v, present := fields[key]; present {
			if _, ok := v.(bool); !ok {
				add(validationBooleanFormat, prefix+"."+key)
			}
		}
	}
}

// validateDiskIOFields validates a diskio check: a required block device name
// and at least one rate predicate.
func validateDiskIOFields(prefix string, fields map[string]any, add addFunc) {
	if cfgval.String(fields[checks.CheckKeyDevice]) == "" {
		add("%s.device is required for a diskio check (e.g. sda, nvme0n1)", prefix)
	}
	validateThresholdPreds(prefix, fields, checks.DiskIOPredFields, add)
}

// validatePressureFields validates a pressure (PSI) check: a required resource
// (cpu, memory or io) and at least one some_*/full_* stall predicate.
func validatePressureFields(prefix string, fields map[string]any, add addFunc) {
	switch cfgval.String(fields[checks.CheckKeyResource]) {
	case checks.PressureResourceCPU, checks.PressureResourceMemory, checks.PressureResourceIO:
	default:
		add("%s.resource must be %s for a pressure check", prefix, checks.PressureResourceSummary)
	}
	validateThresholdPreds(prefix, fields, checks.PressurePredFields, add)
}

// validateCount checks a count entry: a path, an optional `of` kind, an optional
// boolean `recursive`, and exactly one predicate mode: a numeric threshold
// (flat op/value or nested `count: {op, value}`), or growth over a window
// (`delta: {op, value}` plus `within`).
func validateCount(entry map[string]any, path string, add addFunc) {
	if cfgval.String(entry[checks.CheckKeyPath]) == "" {
		add("%s count check requires a path", path)
	}
	if of := cfgval.String(entry[checks.CheckKeyOf]); of != "" {
		if _, ok := countKinds[of]; !ok {
			add("%s count `of` %q is not one of %s", path, of, checks.CountKindSummary)
		}
	}
	if v, present := entry[checks.CheckKeyRecursive]; present {
		if _, ok := v.(bool); !ok {
			add("%s count recursive must be a boolean", path)
		}
	}
	if v, present := entry[checks.CheckKeyIncludeHidden]; present {
		if _, ok := v.(bool); !ok {
			add("%s count include_hidden must be a boolean", path)
		}
	}
	if delta, hasDelta := entry[checks.CheckKeyDelta]; hasDelta {
		if _, hasCount := entry[checks.CheckKeyCount]; hasCount {
			add("%s count check must not mix a count threshold with delta", path)
		}
		_, hasOp := entry[checks.CheckKeyOp]
		_, hasValue := entry[checks.CheckKeyValue]
		if hasOp || hasValue {
			add("%s count check must not mix top-level op/value with delta", path)
		}
		m, ok := delta.(map[string]any)
		if !ok {
			add("%s.delta must be a mapping {op, value}", path)
		} else {
			validateOpNumeric(path+".delta", m, add)
		}
		within := cfgval.String(entry[checks.CheckKeyWithin])
		if within == "" {
			add("%s.within is required when count delta is set (e.g. 2m)", path)
		} else if !isPositiveDuration(within) {
			add("%s.within %q must be a valid positive duration", path, within)
		}
		return
	}
	if cfgval.String(entry[checks.CheckKeyWithin]) != "" {
		add("%s.within requires delta {op, value}", path)
	}
	threshold := entry
	if m, ok := entry[checks.CheckKeyCount].(map[string]any); ok {
		_, hasOp := entry[checks.CheckKeyOp]
		_, hasValue := entry[checks.CheckKeyValue]
		if hasOp || hasValue {
			add("%s count check must not mix a nested count {op, value} with top-level op/value", path)
		}
		threshold = m
	}
	if op := cfgval.String(threshold[checks.CheckKeyOp]); !isValidCompareOp(op) {
		add("%s count check requires a valid op (%s)", path, cfgval.CompareOpSummary)
	}
	if !isNumeric(cfgval.String(threshold[checks.CheckKeyValue])) {
		add("%s count check value %q must be numeric", path, cfgval.String(threshold[checks.CheckKeyValue]))
	}
}
