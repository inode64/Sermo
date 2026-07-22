// Package strutil provides small string-collection helpers shared across
// packages.
package strutil

import "strings"

// Set builds a membership set from values, trimming whitespace and skipping
// blank entries. It returns nil when values is empty.
func Set(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

// MergeUnique appends each non-empty value not already present in list,
// preserving order. Empty strings in either input are skipped.
func MergeUnique(list []string, values ...string) []string {
	seen := make(map[string]struct{}, len(list)+len(values))
	for _, value := range list {
		if value == "" {
			continue
		}
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		list = append(list, value)
	}
	return list
}
