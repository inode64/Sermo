// Package strutil provides small string-collection helpers shared across
// packages.
package strutil

import (
	"fmt"
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
	seen := make(map[string]struct{}, len(values))
	var out []string
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// listSeparator joins the items LimitedList shows.
const listSeparator = ", "

// LimitedList joins at most limit formatted items and makes the omitted count
// explicit ("a, b, +3 more"), so a busy host cannot inflate an event, an API
// response, a dashboard row or an alert message.
func LimitedList[T any](items []T, limit int, format func(T) string) string {
	parts := make([]string, 0, min(len(items), limit)+1)
	for i, item := range items {
		if i >= limit {
			break
		}
		parts = append(parts, format(item))
	}
	if extra := len(items) - limit; extra > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", extra))
	}
	return strings.Join(parts, listSeparator)
}
