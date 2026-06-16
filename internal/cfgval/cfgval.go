// Package cfgval coerces the loosely-typed values produced by decoding YAML into
// map[string]any trees (section 8) into the concrete Go types the rest of Sermo
// needs. Every package decoded config values the same way before; centralizing
// the coercion here keeps one definition per concept so the variants cannot
// drift apart.
package cfgval

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	maxInt = int(^uint(0) >> 1)
	minInt = -maxInt - 1
)

// AsString returns v when it is a string, or "" otherwise. Use it for fields
// that must already be strings; a non-string value is ignored (typically caught
// as a validation error elsewhere). To coerce any scalar to its string form,
// use String instead.
func AsString(v any) string {
	s, _ := v.(string)
	return s
}

// String coerces any scalar — string, bool, integer or float — to its string
// form, returning "" for nil and the fmt %v form for anything else.
func String(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// StringList coerces a scalar-or-list YAML field into a slice of non-empty
// strings: a []any yields its string elements and a bare string yields a
// single-element slice. Non-string elements and empty strings are skipped.
func StringList(v any) []string {
	switch t := v.(type) {
	case []any:
		return nonEmptyStrings(t)
	case string:
		if t != "" {
			return []string{t}
		}
	}
	return nil
}

// StringArray coerces a list YAML field into a slice of non-empty strings.
// Unlike StringList it does not accept a bare string, so it suits fields that
// must be a list (a command's argv, a check's metric counters).
func StringArray(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	return nonEmptyStrings(list)
}

// nonEmptyStrings returns the non-empty string elements of a decoded YAML list,
// dropping non-strings and blanks — the shared body of StringList/StringArray.
func nonEmptyStrings(list []any) []string {
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// StringMap coerces a YAML mapping into a map[string]string, coercing each value
// with String. It returns nil for a non-mapping or an empty mapping.
func StringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		out[k] = String(val)
	}
	return out
}

// Int coerces a scalar — integer, float or decimal string — to an int, reporting
// whether the coercion succeeded. Surrounding whitespace in a string is ignored.
func Int(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int64Value(t)
	case uint64:
		return uint64Value(t)
	case float64:
		n, err := strconv.ParseInt(strconv.FormatFloat(math.Trunc(t), 'f', 0, 64), 10, 0)
		return int(n), err == nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		return n, err == nil
	default:
		return 0, false
	}
}

func int64Value(n int64) (int, bool) {
	if n < int64(minInt) || n > int64(maxInt) {
		return 0, false
	}
	return int(n), true
}

func uint64Value(n uint64) (int, bool) {
	if n > uint64(maxInt) {
		return 0, false
	}
	return int(n), true
}

type byteSizeSuffix struct {
	text string
	mult float64
}

var byteSizeSuffixes = [...]byteSizeSuffix{
	{"TIB", 1 << 40}, {"TB", 1 << 40}, {"T", 1 << 40},
	{"GIB", 1 << 30}, {"GB", 1 << 30}, {"G", 1 << 30},
	{"MIB", 1 << 20}, {"MB", 1 << 20}, {"M", 1 << 20},
	{"KIB", 1 << 10}, {"KB", 1 << 10}, {"K", 1 << 10},
	{"B", 1},
}

// ByteSize parses a scalar byte size. It requires an explicit suffix using
// binary units: K/M/G/T, with optional trailing B or iB ("5G", "5GB", "5GiB").
// Unitless values are rejected so disk thresholds cannot be confused with
// percentage thresholds.
func ByteSize(v any) (uint64, bool) {
	s := strings.TrimSpace(String(v))
	if s == "" {
		return 0, false
	}
	upper := strings.ToUpper(s)
	unit := float64(1)
	hasUnit := false
	for _, suffix := range byteSizeSuffixes {
		if strings.HasSuffix(upper, suffix.text) {
			unit = suffix.mult
			hasUnit = true
			s = strings.TrimSpace(s[:len(s)-len(suffix.text)])
			break
		}
	}
	if !hasUnit {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	bytes := n * unit
	if err != nil || n < 0 || math.IsNaN(bytes) || math.IsInf(bytes, 0) || bytes > float64(^uint64(0)) {
		return 0, false
	}
	return uint64(bytes), true
}

// Float reads a numeric config value that may decode as a YAML int, float or
// string, reporting whether it parsed.
func Float(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint64:
		return float64(t), true
	case float64:
		return t, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// CompareFloat evaluates one `a op b` comparison using the IsCompareOp
// vocabulary; an unknown op never holds. Checks, watches and rules all
// evaluate thresholds through this one definition.
func CompareFloat(a float64, op string, b float64) bool {
	switch op {
	case ">=":
		return a >= b
	case ">":
		return a > b
	case "<=":
		return a <= b
	case "<":
		return a < b
	case "==":
		return a == b
	case "!=":
		return a != b
	default:
		return false
	}
}

// IsCompareOp reports whether op is one of the comparison operators every
// {op, value} threshold accepts (>= > <= < == !=). Shared by config validation
// and the runtime check builders so the threshold grammar cannot drift between
// the two layers. The =~/contains extensions used by response assertions are a
// separate, wider set (see internal/checks compareValue).
func IsCompareOp(op string) bool {
	switch op {
	case ">=", ">", "<=", "<", "==", "!=":
		return true
	default:
		return false
	}
}

// IsAssertOp reports whether op is one of the response-assertion operators:
// the IsCompareOp set plus `contains` (substring) and `=~` (RE2 regexp).
// Shared by the expect/expect_json/sql-style validations and the runtime
// matchers so the assertion vocabulary cannot drift.
func IsAssertOp(op string) bool {
	switch op {
	case "contains", "=~":
		return true
	default:
		return IsCompareOp(op)
	}
}

// Disabled reports whether a config entry opts out via `enabled: false`. An
// absent or non-boolean `enabled` means enabled — the shared reading across
// checks, rules, watches, services and diagnostics.
func Disabled(entry map[string]any) bool {
	b, ok := entry["enabled"].(bool)
	return ok && !b
}

// Bool returns v when it is a bool, or false otherwise.
func Bool(v any) bool {
	b, _ := v.(bool)
	return b
}

// Duration parses a Go duration string (e.g. "30s"), returning 0 when v is not a
// string or not a valid duration. For a non-zero fallback, use DurationOr.
func Duration(v any) time.Duration {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

// DurationOr parses a Go duration string, returning fallback when v is absent,
// not a string, or not a valid duration.
func DurationOr(v any, fallback time.Duration) time.Duration {
	s := AsString(v)
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}
