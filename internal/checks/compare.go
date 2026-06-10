package checks

import (
	"fmt"
	"regexp"
	"sermo/internal/cfgval"
	"strconv"
	"strings"
)

// compareValue evaluates "result op value" and is shared by the sql and http
// checks. Ordering ops (> >= < <=) parse both sides as floats; == and != compare
// numerically when both parse as numbers, otherwise as strings (equal/different);
// =~ matches result against value as a Go (RE2) regular expression.
func compareValue(result, op, value string) (bool, error) {
	switch op {
	case ">", ">=", "<", "<=":
		rf, err := strconv.ParseFloat(strings.TrimSpace(result), 64)
		if err != nil {
			return false, fmt.Errorf("result %q is not numeric for op %s", result, op)
		}
		vf, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return false, fmt.Errorf("value %q is not numeric", value)
		}
		return compareFloat(rf, op, vf), nil
	case "==", "!=":
		if rf, err := strconv.ParseFloat(strings.TrimSpace(result), 64); err == nil {
			if vf, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
				return compareFloat(rf, op, vf), nil
			}
		}
		if op == "==" {
			return result == value, nil
		}
		return result != value, nil
	case "=~":
		re, err := regexp.Compile(value)
		if err != nil {
			return false, fmt.Errorf("invalid regex %q: %v", value, err)
		}
		return re.MatchString(result), nil
	default:
		return false, fmt.Errorf("unsupported op %q", op)
	}
}

// validCompareOp reports whether op is a supported comparison operator.
func validCompareOp(op string) bool {
	switch op {
	case "==", "!=", ">", ">=", "<", "<=", "=~":
		return true
	default:
		return false
	}
}

// OutputMatcher matches captured command/hook output (stdout or stderr) against
// an expectation declared in YAML: a plain string is a substring requirement; an
// {op, value} mapping is an operator comparison (==, !=, >, >=, <, <=, =~) on the
// trimmed output, the same grammar as an http check's expect_body. The zero value
// is inactive and matches anything.
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
		op := cfgval.AsString(t["op"])
		if !validCompareOp(op) {
			return OutputMatcher{}, "op must be one of ==, !=, >, >=, <, <=, =~"
		}
		return OutputMatcher{Op: op, Value: cfgval.String(t["value"])}, ""
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

// parseExpectLatency reads an optional `expect_latency: {op, value}` field shared
// by the http and connection checks. It returns the operator and value (empty op
// when the field is absent) or a warning when the operator is invalid.
func parseExpectLatency(entry map[string]any) (op, value, warn string) {
	lat, ok := entry["expect_latency"].(map[string]any)
	if !ok {
		return "", "", ""
	}
	op = cfgval.AsString(lat["op"])
	if !validCompareOp(op) {
		return "", "", "expect_latency op must be one of ==, !=, >, >=, <, <=, =~"
	}
	return op, cfgval.String(lat["value"]), ""
}
