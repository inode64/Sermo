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
