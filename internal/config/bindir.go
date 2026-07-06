package config

import "strings"

// bindirMarker is the built-in ${bindir} reference. Unlike ${arch}/${os} (a
// 1:1 token substitution), ${bindir} expands a single path value into the list
// of candidate paths across the standard binary search directories, so a catalog
// service can write `binary: ${bindir}/mysqld` instead of repeating the same directory
// list by hand. The existing first-existing-path machinery (firstExistingPath)
// then selects whichever candidate is installed.
const bindirMarker = "${bindir}"

// binDirSearch is the ordered set of directories ${bindir} expands to. Order
// only decides ties when a basename exists in more than one directory; a binary
// normally lives in exactly one, so the resolved path is independent of order.
var binDirSearch = []string{"/usr/bin", "/usr/sbin", "/usr/local/bin", "/usr/local/sbin"}

// expandBindir rewrites ${bindir}-prefixed values in every document's variables
// section (and the global defaults.variables) into the explicit candidate list
// across binDirSearch. It runs at load time, after bakeBuiltins and before
// version-template materialization and validation, so downstream code only ever
// sees concrete absolute paths.
func (c *Config) expandBindir() {
	for _, doc := range c.docs {
		expandBindirVariables(doc.Body)
	}
	if c.Global.Raw != nil {
		if defaults, ok := c.Global.Raw["defaults"].(map[string]any); ok {
			expandBindirVariables(defaults)
		}
	}
}

// expandBindirVariables expands ${bindir} across the `variables` section of a
// single document body, in place. Only variables are touched: that is where
// binaries are declared and from where the rest of a document references them
// via ${binary}.
func expandBindirVariables(body map[string]any) {
	vars, ok := body[sectionVariables].(map[string]any)
	if !ok {
		return
	}
	for k, v := range vars {
		vars[k] = expandBindirValue(v)
	}
}

// expandBindirValue expands ${bindir} in a single variable value. A string that
// contains the marker becomes a candidate list; a list has each element expanded
// and the results concatenated in order; any other value is returned unchanged.
func expandBindirValue(v any) any {
	switch t := v.(type) {
	case string:
		cands := bindirCandidates(t)
		if cands == nil {
			return t
		}
		return bindirCandidateValues(cands)
	case []any:
		return expandBindirList(t)
	default:
		return t
	}
}

func bindirCandidateValues(cands []string) []any {
	out := make([]any, len(cands))
	for i, c := range cands {
		out[i] = c
	}
	return out
}

func expandBindirList(values []any) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		switch expanded := expandBindirValue(value).(type) {
		case []any:
			out = append(out, expanded...)
		default:
			out = append(out, expanded)
		}
	}
	return out
}

// bindirCandidates returns one candidate path per binDirSearch entry, replacing
// every ${bindir} occurrence in s with that directory. It returns nil when s
// does not contain the marker, so non-path values pass through untouched.
func bindirCandidates(s string) []string {
	if !strings.Contains(s, bindirMarker) {
		return nil
	}
	out := make([]string, 0, len(binDirSearch))
	for _, dir := range binDirSearch {
		out = append(out, strings.ReplaceAll(s, bindirMarker, dir))
	}
	return out
}
