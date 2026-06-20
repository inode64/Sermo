package checks

import (
	"fmt"
	"regexp"
	"sermo/internal/cfgval"
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
	case "contains":
		return strings.Contains(result, value), nil
	case ">", ">=", "<", "<=":
		rf, err := parseNumericString("result", result)
		if err != nil {
			return false, fmt.Errorf("%w for op %s", err, op)
		}
		vf, err := parseNumericString("value", value)
		if err != nil {
			return false, err
		}
		return compareFloat(rf, op, vf), nil
	case "==", "!=":
		rf, rerr := parseNumericString("result", result)
		vf, verr := parseNumericString("value", value)
		if rerr == nil && verr == nil {
			return compareFloat(rf, op, vf), nil
		}
		if op == "==" {
			return result == value, nil
		}
		return result != value, nil
	case "=~":
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
	f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
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
		op := cfgval.AsString(t["op"])
		if !validCompareOp(op) {
			return OutputMatcher{}, "op must be one of ==, !=, >, >=, <, <=, contains, =~"
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
		return "", "", "expect_latency op must be one of ==, !=, >, >=, <, <=, contains, =~"
	}
	return op, cfgval.String(lat["value"]), ""
}
