package config

import (
	"slices"
	"strings"
)

// LineDiff returns the lines present only in base and only in other. Both
// inputs are expected to be deterministic key-sorted YAML renders, so this is
// a readable approximation of a structural diff — shared by `sermoctl config
// diff` and the web UI's config view.
func LineDiff(base, other string) (removed, added []string) {
	baseSet := lineCount(base)
	otherSet := lineCount(other)
	for _, l := range strings.Split(strings.TrimRight(base, "\n"), "\n") {
		if otherSet[l] == 0 && !slices.Contains(removed, l) {
			removed = append(removed, l)
		}
	}
	for _, l := range strings.Split(strings.TrimRight(other, "\n"), "\n") {
		if baseSet[l] == 0 && !slices.Contains(added, l) {
			added = append(added, l)
		}
	}
	return removed, added
}

func lineCount(s string) map[string]int {
	out := map[string]int{}
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		out[l]++
	}
	return out
}
