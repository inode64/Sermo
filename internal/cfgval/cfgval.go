// Package cfgval coerces the loosely-typed values produced by decoding YAML into
// map[string]any trees (section 8) into the concrete Go types the rest of Sermo
// needs. Every package decoded config values the same way before; centralizing
// the coercion here keeps one definition per concept so the variants cannot
// drift apart.
package cfgval

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
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
