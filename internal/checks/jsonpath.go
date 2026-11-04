package checks

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// jsonAssertion is one JSON check: the value at a path compared to value with op
// (== by default; also != > >= < <= contains =~). keys is the path compiled by
// ParseJSONPath for a JSON document (http expect_json, command expect_json); a
// connection check's expect names a flat Result field by path and leaves it nil.
type jsonAssertion struct {
	path string
	keys []string
	valueMatcher
}

// ParseJSONPath splits a JSON path into the object keys it walks. Keys are
// separated by dots ("data.status"); a key that itself contains a dot is written
// as a double-quoted string in brackets, as vinylstat names its counters:
// counters["MAIN.backend_fail"].value. Array indexes are not supported.
func ParseJSONPath(path string) ([]string, error) {
	if path == "" {
		return nil, errors.New("JSON path is empty")
	}
	var keys []string
	for rest, first := path, true; rest != ""; first = false {
		if strings.HasPrefix(rest, "[") {
			key, after, err := cutJSONPathBracket(rest)
			if err != nil {
				return nil, fmt.Errorf("JSON path %q: %w", path, err)
			}
			keys, rest = append(keys, key), after
			continue
		}
		if !first {
			after, ok := strings.CutPrefix(rest, ".")
			if !ok {
				return nil, fmt.Errorf("JSON path %q: a bracketed key must be followed by . or [", path)
			}
			rest = after
		}
		end := strings.IndexAny(rest, ".[]")
		if end < 0 {
			end = len(rest)
		}
		if end < len(rest) && rest[end] == ']' {
			return nil, fmt.Errorf("JSON path %q: ] without a matching [", path)
		}
		if end == 0 {
			return nil, fmt.Errorf("JSON path %q has an empty key", path)
		}
		keys, rest = append(keys, rest[:end]), rest[end:]
	}
	return keys, nil
}

// cutJSONPathBracket reads one ["key"] segment at the start of s and returns the
// unquoted key and what follows the closing bracket.
func cutJSONPathBracket(s string) (key, rest string, err error) {
	body, ok := strings.CutPrefix(s, `["`)
	if !ok {
		return "", "", errors.New(`[ must hold a double-quoted key, as in ["a.b"]`)
	}
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '\\':
			i++ // the escaped byte cannot close the string
		case '"':
			quoted := `"` + body[:i+1]
			key, err := strconv.Unquote(quoted)
			if err != nil {
				return "", "", fmt.Errorf("invalid quoted key %s", quoted)
			}
			rest, closed := strings.CutPrefix(body[i+1:], "]")
			if !closed {
				return "", "", fmt.Errorf("quoted key %s must be closed by ]", quoted)
			}
			return key, rest, nil
		}
	}
	return "", "", errors.New("unterminated quoted key")
}

// parseJSONAssertions reads an expect_json mapping and compiles every path, so a
// malformed one is a build error rather than a lookup that never matches.
func parseJSONAssertions(v any) ([]jsonAssertion, string) {
	assertions, warn := parseAssertionMap(v, CheckKeyExpectJSON)
	if warn != "" {
		return nil, warn
	}
	for i := range assertions {
		keys, err := ParseJSONPath(assertions[i].path)
		if err != nil {
			return nil, CheckKeyExpectJSON + ": " + err.Error()
		}
		assertions[i].keys = keys
	}
	return assertions, ""
}

// jsonAssertionFailure checks every assertion against a decoded JSON document and
// describes the first that does not hold ("" when all do). A missing path fails:
// the document is the check's own evidence, so an absent field is a wrong answer.
func jsonAssertionFailure(doc any, assertions []jsonAssertion) string {
	for _, a := range assertions {
		got, ok := jsonLookup(doc, a.keys)
		if !ok {
			return fmt.Sprintf("json %q missing", a.path)
		}
		ok, err := a.compare(jsonValueString(got))
		if err != nil {
			return fmt.Sprintf("json %q: %v", a.path, err)
		}
		if !ok {
			return fmt.Sprintf("json %q %s %q (got %q)", a.path, a.op, a.value, jsonValueString(got))
		}
	}
	return ""
}

// jsonLookup walks keys through the nested objects of a decoded JSON document.
func jsonLookup(doc any, keys []string) (any, bool) {
	cur := doc
	for _, key := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// jsonPath looks up a plain dotted path (e.g. "data.status"), the form summary
// templates name.
func jsonPath(doc any, path string) (any, bool) {
	return jsonLookup(doc, strings.Split(path, "."))
}

// jsonValueString renders a decoded JSON scalar for comparison with the expected
// (string) value from config.
func jsonValueString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, floatFormatFixed, floatPrecisionAuto, numericBits64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%v", t)
	}
}
