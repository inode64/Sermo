package checks

import (
	"fmt"
	"maps"
	"regexp"
	"sermo/internal/cfgval"
	"slices"
	"strconv"
	"strings"
)

// compareValue evaluates "result op value" and is shared by the sql, http and
// connection checks. Ordering ops (> >= < <=) parse both sides as floats; == and
// != compare numerically when both parse as numbers, otherwise as strings
// (equal/different); contains requires value to be a substring of result; =~
// matches result against value as a Go (RE2) regular expression. The set
// matches expect_json (jsonAssert) so every {op, value} comparison shares one
// vocabulary.
func compareValue(result, op, value string) (bool, error) {
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
		return compareFloat(rf, op, vf), nil
	case cfgval.CompareOpEqual, cfgval.CompareOpNotEqual:
		rf, rerr := parseNumericString(CheckKeyResult, result)
		vf, verr := parseNumericString(CheckKeyValue, value)
		if rerr == nil && verr == nil {
			return compareFloat(rf, op, vf), nil
		}
		if op == cfgval.CompareOpEqual {
			return result == value, nil
		}
		return result != value, nil
	case cfgval.AssertOpRegex:
		re, err := regexp.Compile(value)
		if err != nil {
			return false, fmt.Errorf("invalid regex %q: %w", value, err)
		}
		return re.MatchString(result), nil
	default:
		return false, fmt.Errorf("unsupported op %q", op)
	}
}

func parseNumericString(label, value string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(value), numericBits64)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not numeric", label, value)
	}
	return f, nil
}

// validCompareOp reports whether op is a supported comparison operator (the
// shared assertion set in cfgval).
func validCompareOp(op string) bool {
	return cfgval.IsAssertOp(op)
}

// OutputMatcher matches captured command/hook output (stdout or stderr) against
// an expectation declared in YAML: a plain string is a substring requirement; an
// {op, value} mapping is an operator comparison (==, !=, >, >=, <, <=, contains,
// =~) on the trimmed output, using the same operator set as http expect_body.
// The zero value is inactive and matches anything.
type OutputMatcher struct {
	Substring string // non-empty: output must contain this
	Op        string // non-empty: compareValue(trimmed output, Op, Value)
	Value     string
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
		op := cfgval.AsString(t[CheckKeyOp])
		if !validCompareOp(op) {
			return OutputMatcher{}, "op must be one of " + cfgval.AssertOpSummary
		}
		value := cfgval.String(t[CheckKeyValue])
		if err := ValidateAssertionValue("", op, value); err != nil {
			return OutputMatcher{}, err.Error()
		}
		return OutputMatcher{Op: op, Value: value}, ""
	default:
		return OutputMatcher{}, "must be a string substring or an {op, value} mapping"
	}
}

// Active reports whether the matcher carries an expectation.
func (m OutputMatcher) Active() bool { return m.Substring != "" || m.Op != "" }

// Match evaluates output against the matcher. ok is true when the expectation is
// satisfied (or none is set); detail describes the mismatch for a result message.
func (m OutputMatcher) Match(output string) (ok bool, detail string) {
	if m.Substring != "" && !strings.Contains(output, m.Substring) {
		return false, fmt.Sprintf("does not contain %q", m.Substring)
	}
	if m.Op != "" {
		res, err := compareValue(strings.TrimSpace(output), m.Op, m.Value)
		if err != nil {
			return false, err.Error()
		}
		if !res {
			return false, fmt.Sprintf("%s %q not satisfied", m.Op, m.Value)
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
	Regex    []string
	regexps  []*regexp.Regexp
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
				matcher.Regex = append(matcher.Regex, value)
				matcher.regexps = append(matcher.regexps, re)
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
	return len(m.Contains) > 0 || len(m.Excludes) > 0 || len(m.Regex) > 0
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
	regexps := m.regexps
	if len(regexps) != len(m.Regex) {
		regexps = make([]*regexp.Regexp, 0, len(m.Regex))
		for _, value := range m.Regex {
			re, err := regexp.Compile(value)
			if err != nil {
				return false, fmt.Sprintf("regex %q is not valid: %v", value, err)
			}
			regexps = append(regexps, re)
		}
	}
	for i, re := range regexps {
		if !re.MatchString(output) {
			return false, fmt.Sprintf("does not match regex %q", m.Regex[i])
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
	op = cfgval.AsString(lat[CheckKeyOp])
	if !validCompareOp(op) {
		return "", "", "expect_latency op must be one of " + cfgval.AssertOpSummary
	}
	value = cfgval.String(lat[CheckKeyValue])
	if err := ValidateAssertionValue(CheckKeyExpectLatency, op, value); err != nil {
		return "", "", err.Error()
	}
	return op, value, ""
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
