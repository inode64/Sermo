package config

import (
	"slices"
	"testing"
)

func TestLineDiff(t *testing.T) {
	base := "a: 1\nb: 2\nc: 3\n"
	other := "a: 1\nb: 22\nd: 4\n"
	removed, added := LineDiff(base, other)
	if want := []string{"b: 2", "c: 3"}; !slices.Equal(removed, want) {
		t.Fatalf("removed = %v, want %v", removed, want)
	}
	if want := []string{"b: 22", "d: 4"}; !slices.Equal(added, want) {
		t.Fatalf("added = %v, want %v", added, want)
	}
}

func TestLineDiffIdentical(t *testing.T) {
	s := "a: 1\nb: 2\n"
	if removed, added := LineDiff(s, s); len(removed) != 0 || len(added) != 0 {
		t.Fatalf("identical inputs diffed: removed=%v added=%v", removed, added)
	}
}

func TestLineDiffDedupesRepeatedLines(t *testing.T) {
	// A line repeated in base is reported once, not per occurrence.
	removed, _ := LineDiff("x\nx\ny\n", "y\n")
	if want := []string{"x"}; !slices.Equal(removed, want) {
		t.Fatalf("removed = %v, want %v (deduped)", removed, want)
	}
}

func TestLineDiffTrailingNewlinesAndBlanks(t *testing.T) {
	// Trailing \n are treated as insignificant whitespace for diff (consistent
	// TrimRight now in both lineCount and iteration); internal blank lines are
	// significant and reported. This guards against phantom "" entries or
	// asymmetric trailing blank diffs (the prior inconsistency in count vs split).
	removed, added := LineDiff("a: 1\n\nb: 2\n\n\n", "a: 1\nb: 2\nc: 3\n")
	if wantR := []string{""}; !slices.Equal(removed, wantR) { // the internal blank after a:1
		t.Fatalf("removed = %v, want %v (blank line)", removed, wantR)
	}
	if wantA := []string{"c: 3"}; !slices.Equal(added, wantA) {
		t.Fatalf("added = %v, want %v", added, wantA)
	}

	// Pure trailing nl differences produce no diff (no phantom entries).
	if r, a := LineDiff("x: 1\n", "x: 1\n\n"); len(r) != 0 || len(a) != 0 {
		t.Fatalf("trailing-only nl diff: r=%v a=%v want empty", r, a)
	}
}
