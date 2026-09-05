// Package strutil provides small string-collection helpers shared across
// packages.
package strutil

import (
	"maps"
	"slices"
	"strings"
)

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

// SortedUnique returns the trimmed, non-empty values once in lexical order.
// An empty result is nil.
func SortedUnique(values []string) []string {
	return slices.Sorted(maps.Keys(Set(values)))
}

// Unique returns the non-empty values once, preserving first-seen order.
// Unlike SortedUnique it does not trim or sort. An empty result is nil.
func Unique(values []string) []string {
	return mergeUnique(nil, values...)
}

// mergeUnique appends each non-empty value not already present in list,
// preserving order. Empty strings already in list stay; empty extras are skipped.
func mergeUnique(list []string, values ...string) []string {
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
