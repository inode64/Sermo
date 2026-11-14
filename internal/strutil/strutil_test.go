package strutil

import (
	"maps"
	"slices"
	"strconv"
	"testing"
)

func TestSet(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want map[string]struct{}
	}{
		{name: "empty input returns nil", in: nil, want: nil},
		{name: "values trimmed", in: []string{" a ", "b"}, want: map[string]struct{}{"a": {}, "b": {}}},
		{name: "blank entries skipped", in: []string{"", "  ", "c"}, want: map[string]struct{}{"c": {}}},
		{name: "duplicates collapse", in: []string{"d", "d "}, want: map[string]struct{}{"d": {}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Set(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("Set(%v) = %v, want nil", tc.in, got)
				}
				return
			}
			if !maps.Equal(got, tc.want) {
				t.Fatalf("Set(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSortedUnique(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   []string
	}{
		{name: "nil"},
		{name: "empty and blank", values: []string{"", "  "}},
		{name: "trim deduplicate and sort", values: []string{" zed ", "ana", "zed", "  bob", "ana "}, want: []string{"ana", "bob", "zed"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SortedUnique(tc.values); !slices.Equal(got, tc.want) {
				t.Fatalf("SortedUnique(%v) = %v, want %v", tc.values, got, tc.want)
			}
		})
	}
}

func TestUnique(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   []string
	}{
		{name: "nil"},
		{name: "empty strings skipped", values: []string{"", "a", ""}, want: []string{"a"}},
		{name: "first-seen order", values: []string{"b", "a", "b", "c"}, want: []string{"b", "a", "c"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Unique(tc.values); !slices.Equal(got, tc.want) {
				t.Fatalf("Unique(%v) = %v, want %v", tc.values, got, tc.want)
			}
		})
	}
}

func TestLimitedList(t *testing.T) {
	tests := []struct {
		name  string
		items []int
		want  string
	}{
		{name: "nil"},
		{name: "under the limit", items: []int{1, 2}, want: "#1, #2"},
		{name: "at the limit", items: []int{1, 2, 3}, want: "#1, #2, #3"},
		{name: "over the limit counts the rest", items: []int{1, 2, 3, 4, 5}, want: "#1, #2, #3, +2 more"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := LimitedList(tc.items, 3, func(n int) string { return "#" + strconv.Itoa(n) })
			if got != tc.want {
				t.Fatalf("LimitedList(%v, 3) = %q, want %q", tc.items, got, tc.want)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	got := []string{"10.0", "8.3", "8.11", "9", "", "8.3.1"}
	slices.SortFunc(got, CompareVersions)
	if want := []string{"", "8.3", "8.3.1", "8.11", "9", "10.0"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"3.14", "3.13", 1},
		{"3.10", "3.9", 1},
		{"3.13", "3.13", 0},
		{"", "", 0},
		{"", "1", -1},
		// A non-numeric segment compares as a string, keeping the order total.
		{"8.3-rc2", "8.3-rc1", 1},
		{"8.3", "8.3-rc1", -1},
	} {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
