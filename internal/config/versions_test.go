package config

import (
	"sort"
	"testing"
)

func TestVersionLessOrdersNumerically(t *testing.T) {
	got := []string{"10.0", "8.3", "8.11", "9", "", "8.3.1"}
	sort.Slice(got, func(i, j int) bool { return versionLess(got[i], got[j]) })

	want := []string{"", "8.3", "8.3.1", "8.11", "9", "10.0"}
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("version order wrong:\n got  %v\n want %v", got, want)
		}
	}
}

func TestVersionLessNonNumericSuffix(t *testing.T) {
	// Non-numeric trailing segments fall back to a lexicographic compare on
	// that segment, keeping the comparator total and stable.
	if versionLess("8.3-rc2", "8.3-rc1") {
		t.Fatal("8.3-rc2 must not sort before 8.3-rc1")
	}
	if !versionLess("8.3", "8.3-rc1") {
		t.Fatal("8.3 (shorter) must sort before 8.3-rc1")
	}
}
