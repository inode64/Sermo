package cfgval

import (
	"testing"
	"time"
)

func TestAsString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"hello", "hello"},
		{"", ""},
		{nil, ""},
		{42, ""},      // non-string ignored
		{true, ""},    // non-string ignored
		{3.14, ""},    // non-string ignored
		{[]any{}, ""}, // non-string ignored
	}
	for _, c := range cases {
		if got := AsString(c.in); got != c.want {
			t.Errorf("AsString(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"hello", "hello"},
		{"", ""},
		{nil, ""},
		{42, "42"},
		{int64(-7), "-7"},
		{uint64(9), "9"},
		{3.5, "3.5"},
		{1.0, "1"}, // trailing zeros trimmed by FormatFloat -1 precision
		{true, "true"},
		{false, "false"},
		{[]any{1, 2}, "[1 2]"},               // non-scalar falls back to %v
		{map[string]any{"a": 1}, "map[a:1]"}, // non-scalar falls back to %v
	}
	for _, c := range cases {
		if got := String(c.in); got != c.want {
			t.Errorf("String(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStringList(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"list of strings", []any{"a", "b"}, []string{"a", "b"}},
		{"skips non-strings and empties", []any{"a", "", 7, "b"}, []string{"a", "b"}},
		{"bare string becomes single element", "solo", []string{"solo"}},
		{"empty bare string is nil", "", nil},
		{"nil is nil", nil, nil},
		{"non-list non-string is nil", 42, nil},
		{"empty list is empty (non-nil)", []any{}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StringList(c.in); !eqStrs(got, c.want) {
				t.Errorf("StringList(%#v) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestStringArray(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"list of strings", []any{"a", "b"}, []string{"a", "b"}},
		{"skips non-strings and empties", []any{"a", "", 7, "b"}, []string{"a", "b"}},
		{"bare string is NOT accepted", "solo", nil},
		{"nil is nil", nil, nil},
		{"empty list is empty (non-nil)", []any{}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StringArray(c.in); !eqStrs(got, c.want) {
				t.Errorf("StringArray(%#v) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestStringMap(t *testing.T) {
	got := StringMap(map[string]any{"a": "x", "n": 5, "b": true})
	want := map[string]string{"a": "x", "n": "5", "b": "true"}
	if len(got) != len(want) {
		t.Fatalf("StringMap len = %d, want %d (%#v)", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("StringMap[%q] = %q, want %q", k, got[k], v)
		}
	}
	if StringMap(nil) != nil {
		t.Error("StringMap(nil) should be nil")
	}
	if StringMap(map[string]any{}) != nil {
		t.Error("StringMap(empty) should be nil")
	}
	if StringMap("not a map") != nil {
		t.Error("StringMap(non-map) should be nil")
	}
}

func TestInt(t *testing.T) {
	cases := []struct {
		in   any
		want int
		ok   bool
	}{
		{5, 5, true},
		{int64(6), 6, true},
		{uint64(7), 7, true},
		{8.9, 8, true}, // float truncates
		{"10", 10, true},
		{"  12  ", 12, true}, // whitespace trimmed
		{"nope", 0, false},
		{"", 0, false},
		{nil, 0, false},
		{true, 0, false},
	}
	for _, c := range cases {
		got, ok := Int(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("Int(%#v) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestByteSize(t *testing.T) {
	cases := []struct {
		in   any
		want uint64
		ok   bool
	}{
		{"1024", 0, false},
		{1024, 0, false},
		{"1K", 1 << 10, true},
		{"1KB", 1 << 10, true},
		{"1KiB", 1 << 10, true},
		{"1B", 1, true},
		{"1.5G", 1536 << 20, true},
		{"2T", 2 << 40, true},
		{"0", 0, false},
		{"0G", 0, true},
		{"", 0, false},
		{"-1G", 0, false},
		{"NaN", 0, false},
		{"10P", 0, false},
		{true, 0, false},
	}
	for _, c := range cases {
		got, ok := ByteSize(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ByteSize(%#v) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestBool(t *testing.T) {
	if !Bool(true) {
		t.Error("Bool(true) = false")
	}
	for _, in := range []any{false, nil, "true", 1, 0} {
		if Bool(in) {
			t.Errorf("Bool(%#v) = true, want false", in)
		}
	}
}

func TestDuration(t *testing.T) {
	cases := []struct {
		in   any
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"1h30m", 90 * time.Minute},
		{"bad", 0},
		{"", 0},
		{nil, 0},
		{42, 0}, // non-string
	}
	for _, c := range cases {
		if got := Duration(c.in); got != c.want {
			t.Errorf("Duration(%#v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDurationOr(t *testing.T) {
	fb := 5 * time.Second
	cases := []struct {
		in   any
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"", fb},
		{nil, fb},
		{"bad", fb},
		{42, fb}, // non-string -> AsString "" -> fallback
	}
	for _, c := range cases {
		if got := DurationOr(c.in, fb); got != c.want {
			t.Errorf("DurationOr(%#v) = %v, want %v", c.in, got, fb)
		}
	}
}
