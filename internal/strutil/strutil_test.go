package strutil

import (
	"maps"
	"slices"
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

func TestMergeUnique(t *testing.T) {
	cases := []struct {
		name   string
		list   []string
		values []string
		want   []string
	}{
		{name: "dedupe onto nil list", list: nil, values: []string{"a", "b", "a"}, want: []string{"a", "b"}},
		{name: "order preserved", list: []string{"b"}, values: []string{"a", "b", "c"}, want: []string{"b", "a", "c"}},
		{name: "empties skipped", list: []string{"a", ""}, values: []string{"", "d"}, want: []string{"a", "", "d"}},
		{name: "no values keeps list", list: []string{"x"}, values: nil, want: []string{"x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MergeUnique(tc.list, tc.values...); !slices.Equal(got, tc.want) {
				t.Fatalf("MergeUnique(%v, %v) = %v, want %v", tc.list, tc.values, got, tc.want)
			}
		})
	}
}
