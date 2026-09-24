package checks

import (
	"fmt"
	"maps"
	"regexp"
	"sermo/internal/cfgval"
	"slices"
	"strconv"
	"strings"
	"time"
)

// valueMatcher evaluates "result op value" and is shared by the sql, http and
// connection checks, including HTTP expect_json after jsonValueString. Ordering
// ops (> >= < <=) parse both sides as floats; == and != compare numerically when
// both parse as numbers, otherwise as strings (equal/different); contains
// requires value to be a substring of result; =~ matches result against value as
// a Go (RE2) regular expression. Parse and regex failures return an error so
// every {op, value} comparison shares one vocabulary and one diagnostic.
type valueMatcher struct {
	op, value string
	regex     *regexp.Regexp
	err       error
}

func newValueMatcher(op, value string) valueMatcher {
	m := valueMatcher{op: op, value: value}
	if op == cfgval.AssertOpRegex {
		m.regex, m.err = regexp.Compile(value)
		if m.err != nil {
			m.err = fmt.Errorf("invalid regex %q: %w", value, m.err)
		}
	}
	return m
}

func (m valueMatcher) compare(result string) (bool, error) {
	if m.err != nil {
		return false, m.err
	}
	op, value := m.op, m.value
	switch op {
	case cfgval.AssertOpContains:
		return strings.Contains(result, value), nil
	case cfgval.CompareOpGreater, cfgval.CompareOpGreaterEqual, cfgval.CompareOpLess, cfgval.CompareOpLessEqual:
		rf, err := parseNumericString(CheckKeyResult, result)
		if err != nil {
			return false, fmt.Errorf("%w for op %s", err, op)
		}
		vf, err := parseNumericString(CheckKeyValue, value)
		if err != nil {
			return false, err
		}
		return cfgval.CompareFloat(rf, op, vf), nil
	case cfgval.CompareOpEqual, cfgval.CompareOpNotEqual:
		rf, rerr := parseNumericString(CheckKeyResult, result)
		vf, verr := parseNumericString(CheckKeyValue, value)
		if rerr == nil && verr == nil {
			return cfgval.CompareFloat(rf, op, vf), nil
		}
		if op == cfgval.CompareOpEqual {
			return result == value, nil
		}
		return result != value, nil
	case cfgval.AssertOpRegex:
		return m.regex.MatchString(result), nil
	default:
		return false, fmt.Errorf("unsupported op %q", op)
	}
}

// assertOpValue reads and validates the op/value pair shared by the database
// query checks: op must be a known compare op and value a non-empty assertion
// value valid for that op. noun is the check label used in the error strings
// (e.g. "influxdb-query", "mongodb-query"); errMsg is empty on success.
func assertOpValue(entry map[string]any, noun string) (op, value, errMsg string) {
	op, value, err := ParseAssertion(entry, noun+" check", "", true)
	if err != nil {
		return "", "", err.Error()
	}
	return op, value, ""
}

// finishScalarCompare applies the common condition-check comparison and emits
// the standard scalar reading data. Each database check keeps its own I/O and
// supplies only its label and protocol-specific readings.
func finishScalarCompare(b base, label, result string, matcher valueMatcher, start time.Time, data map[string]any) Result {
	op, threshold := matcher.op, matcher.value
	ok, err := matcher.compare(result)
	if err != nil {
		return b.unavailableResult(fmt.Sprintf("%s: %v", label, err), start)
	}
	data[DataKeyOp] = op
	data[DataKeyThreshold] = threshold
	data[DataKeyResult] = result
	if value, err := strconv.ParseFloat(strings.TrimSpace(result), numericBits64); err == nil {
		data[DataKeyValue] = value
	}
	res := b.result(ok, fmt.Sprintf("%s: %q %s %q = %t", label, result, op, threshold, ok), start)
	res.Data = data
	return res
}

func parseNumericString(label, value string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(value), numericBits64)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not numeric", label, value)
	}
	return f, nil
}

// OutputMatcher matches captured command/hook output (stdout or stderr) against
// an expectation declared in YAML: a plain string is a substring requirement; an
// {op, value} mapping is an operator comparison (==, !=, >, >=, <, <=, contains,
// =~) on the trimmed output, using the same operator set as http expect_body.
// The zero value is inactive and matches anything.
type OutputMatcher struct {
	Substring string // non-empty: output must contain this
	assertion valueMatcher
}

// ParseOutputMatcher reads an expect_stdout/expect_stderr field into a matcher: a
// string yields a substring matcher, an {op, value} mapping an operator matcher.
// It returns the matcher and a warning ("" when valid or absent) describing an
// invalid operator or shape.
func ParseOutputMatcher(v any) (OutputMatcher, string) {
	switch t := v.(type) {
	case nil:
		return OutputMatcher{}, ""
	case string:
		return OutputMatcher{Substring: t}, ""
	case map[string]any:
		op, value, err := ParseAssertion(t, "", "", false)
		if err != nil {
			return OutputMatcher{}, err.Error()
		}
		return OutputMatcher{assertion: newValueMatcher(op, value)}, ""
	default:
		return OutputMatcher{}, "must be a string substring or an {op, value} mapping"
	}
}

// Active reports whether the matcher carries an expectation.
func (m OutputMatcher) Active() bool { return m.Substring != "" || m.assertion.op != "" }

// Match evaluates output against the matcher. ok is true when the expectation is
// satisfied (or none is set); detail describes the mismatch for a result message.
func (m OutputMatcher) Match(output string) (ok bool, detail string) {
	if m.Substring != "" && !strings.Contains(output, m.Substring) {
		return false, fmt.Sprintf("does not contain %q", m.Substring)
	}
	if m.assertion.op != "" {
		res, err := m.assertion.compare(strings.TrimSpace(output))
		if err != nil {
			return false, err.Error()
		}
		if !res {
			return false, fmt.Sprintf("%s %q not satisfied", m.assertion.op, m.assertion.value)
		}
	}
	return true, ""
}

// VersionMatcher checks the complete output of a version command against an app
// identity declaration. It is stricter than a generic stdout matcher because a
// successful match proves that a compatibility binary belongs to the expected
// implementation, such as distinguishing MariaDB's mysqld from Oracle MySQL.
type VersionMatcher struct {
	Contains []string
	Excludes []string
	regexps  []versionRegexp
}

type versionRegexp struct {
	pattern string
	re      *regexp.Regexp
}

// ParseVersionMatcher reads a version_match mapping. Supported keys are:
// contains, excludes and regex; each accepts either a string or a non-empty list
// of strings. The zero matcher is inactive and matches anything.
func ParseVersionMatcher(v any) (VersionMatcher, string) {
	if v == nil {
		return VersionMatcher{}, ""
	}
	spec, ok := v.(map[string]any)
	if !ok {
		return VersionMatcher{}, "must be a mapping with " + VersionMatchKeySummary
	}
	var matcher VersionMatcher
	for _, key := range slices.Sorted(maps.Keys(spec)) {
		values := cfgval.StringList(spec[key])
		if len(values) == 0 {
			return VersionMatcher{}, key + " must be a non-empty string or list"
		}
		switch key {
		case VersionMatchKeyContains:
			matcher.Contains = append(matcher.Contains, values...)
		case VersionMatchKeyExcludes:
			matcher.Excludes = append(matcher.Excludes, values...)
		case VersionMatchKeyRegex:
			for _, value := range values {
				re, err := regexp.Compile(value)
				if err != nil {
					return VersionMatcher{}, fmt.Sprintf("regex %q is not valid: %v", value, err)
				}
				matcher.regexps = append(matcher.regexps, versionRegexp{pattern: value, re: re})
			}
		default:
			return VersionMatcher{}, fmt.Sprintf("unknown key %q (expected %s)", key, VersionMatchKeySummary)
		}
	}
	if !matcher.Active() {
		return VersionMatcher{}, "must declare " + VersionMatchKeySummary
	}
	return matcher, ""
}

// Active reports whether the matcher carries an identity expectation.
func (m VersionMatcher) Active() bool {
	return len(m.Contains) > 0 || len(m.Excludes) > 0 || len(m.regexps) > 0
}

// Match evaluates output against the configured identity rules.
func (m VersionMatcher) Match(output string) (ok bool, detail string) {
	if !m.Active() {
		return true, ""
	}
	if strings.TrimSpace(output) == "" {
		return false, "has no version output"
	}
	for _, value := range m.Contains {
		if !strings.Contains(output, value) {
			return false, fmt.Sprintf("does not contain required %q", value)
		}
	}
	for _, value := range m.Excludes {
		if strings.Contains(output, value) {
			return false, fmt.Sprintf("contains excluded %q", value)
		}
	}
	for _, matcher := range m.regexps {
		if !matcher.re.MatchString(output) {
			return false, fmt.Sprintf("does not match regex %q", matcher.pattern)
		}
	}
	return true, ""
}

// VersionOutput joins stdout and stderr for identity matching. Some programs
// print versions to stderr, so matchers must see both streams.
func VersionOutput(stdout, stderr string) string {
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	default:
		return stdout + checkLineSeparator + stderr
	}
}

// parseExpectLatency reads an optional `expect_latency: {op, value}` field shared
// by the http and connection checks. It returns the operator and value (empty op
// when the field is absent) or a warning when the operator is invalid.
func parseExpectLatency(entry map[string]any) (op, value, warn string) {
	lat, ok := entry[CheckKeyExpectLatency].(map[string]any)
	if !ok {
		return "", "", ""
	}
	op, value, err := ParseAssertion(lat, CheckKeyExpectLatency, "", false)
	if err != nil {
		return "", "", err.Error()
	}
	return op, value, ""
}

// ParseAssertion reads and validates an {op, value} comparison. label
// names the containing field in diagnostics; defaultOp applies when op is
// intentionally optional for a specific assertion form. requireValue preserves
// the stricter query-check contract for an explicitly non-empty value.
func ParseAssertion(entry map[string]any, label, defaultOp string, requireValue bool) (op, value string, err error) {
	op = cfgval.AsString(entry[CheckKeyOp])
	if op == "" {
		op = defaultOp
	}
	if !cfgval.IsAssertOp(op) {
		prefix := ""
		if label != "" {
			prefix = label + " "
		}
		return "", "", fmt.Errorf("%sop %q is not one of %s", prefix, op, cfgval.AssertOpSummary)
	}
	value = cfgval.String(entry[CheckKeyValue])
	if requireValue && value == "" {
		return "", "", fmt.Errorf("%s value is required", label)
	}
	if err := ValidateAssertionValue(label, op, value); err != nil {
		return "", "", err
	}
	return op, value, nil
}

// ValidateAssertionValue checks the value side of assertion operators.
func ValidateAssertionValue(label, op, value string) error {
	valueLabel := CheckKeyValue
	if label != "" {
		valueLabel = label + " value"
	}
	switch op {
	case cfgval.CompareOpGreater, cfgval.CompareOpGreaterEqual, cfgval.CompareOpLess, cfgval.CompareOpLessEqual:
		if _, err := strconv.ParseFloat(strings.TrimSpace(value), numericBits64); err != nil {
			return fmt.Errorf("%s %q must be numeric for op %s", valueLabel, value, op)
		}
	case cfgval.AssertOpRegex:
		if _, err := regexp.Compile(value); err != nil {
			return fmt.Errorf("%s is not a valid regexp: %w", valueLabel, err)
		}
	}
	return nil
}

// mustBeMappingSuffix completes a "<field> must be a mapping" build error when a
// check field that must hold a YAML mapping does not. Distinct from config's own
// mapping-validation messages (different construction and surface).
const mustBeMappingSuffix = " must be a mapping"

// parseAssertionMap reads a field -> value/{op,value} mapping into ordered
// assertions. It is shared by HTTP JSON checks and connection-protocol checks.
func parseAssertionMap(v any, field string) ([]jsonAssertion, string) {
	if v == nil {
		return nil, ""
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, field + mustBeMappingSuffix
	}
	if len(m) == 0 {
		return nil, ""
	}
	out := make([]jsonAssertion, 0, len(m))
	for _, path := range slices.Sorted(maps.Keys(m)) {
		raw := m[path]
		if cond, ok := raw.(map[string]any); ok {
			op, value, err := ParseAssertion(cond, field+"."+path, cfgval.CompareOpEqual, false)
			if err != nil {
				return nil, err.Error()
			}
			out = append(out, jsonAssertion{path: path, valueMatcher: newValueMatcher(op, value)})
			continue
		}
		out = append(out, jsonAssertion{path: path, valueMatcher: newValueMatcher(cfgval.CompareOpEqual, cfgval.String(raw))})
	}
	return out, ""
}
