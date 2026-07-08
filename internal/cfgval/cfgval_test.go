package cfgval

import (
	"slices"
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
		{int64(10), "10"}, // FormatInt decimal base (mutant .8)
		{uint64(9), "9"},
		{uint64(10), "10"}, // FormatUint decimal base (mutant .13)
		{3.5, "3.5"},
		{1.234, "1.234"}, // FormatFloat minimum digits (mutant .14 pin)
		{1.0, "1"},       // trailing zeros trimmed by FormatFloat -1 precision
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
			if got := StringList(c.in); !slices.Equal(got, c.want) {
				t.Errorf("StringList(%#v) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestStrictStringList(t *testing.T) {
	cases := []struct {
		name    string
		in      any
		want    []string
		wantErr bool
	}{
		{"list of strings", []any{"a", "b"}, []string{"a", "b"}, false},
		{"skips empties", []any{"a", "", "b"}, []string{"a", "b"}, false},
		{"bare string becomes single element", "solo", []string{"solo"}, false},
		{"empty bare string is nil", "", nil, false},
		{"nil is nil", nil, nil, false},
		{"native string slice is copied", []string{"a", "b"}, []string{"a", "b"}, false},
		{"rejects non-string item", []any{"a", 7}, nil, true},
		{"rejects non-list non-string", 42, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := StrictStringList(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("StrictStringList(%#v) error = %v, wantErr %v", c.in, err, c.wantErr)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("StrictStringList(%#v) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestStrictStringListStringSliceIsCopied(t *testing.T) {
	in := []string{"a"}
	got, err := StrictStringList(in)
	if err != nil {
		t.Fatal(err)
	}
	got[0] = "changed"
	if in[0] != "a" {
		t.Fatalf("StrictStringList reused input slice, got source %#v", in)
	}
}

func TestStrictStringArray(t *testing.T) {
	cases := []struct {
		name    string
		in      any
		want    []string
		wantErr bool
	}{
		{"list of strings", []any{"a", "b"}, []string{"a", "b"}, false},
		{"preserves empties", []any{"a", "", "b"}, []string{"a", "", "b"}, false},
		{"native string slice is copied", []string{"a", "b"}, []string{"a", "b"}, false},
		{"rejects non-string item", []any{"a", 7}, nil, true},
		{"rejects bare string", "solo", nil, true},
		{"rejects nil", nil, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := StrictStringArray(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("StrictStringArray(%#v) error = %v, wantErr %v", c.in, err, c.wantErr)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("StrictStringArray(%#v) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestStrictStringArrayStringSliceIsCopied(t *testing.T) {
	in := []string{"a"}
	got, err := StrictStringArray(in)
	if err != nil {
		t.Fatal(err)
	}
	got[0] = "changed"
	if in[0] != "a" {
		t.Fatalf("StrictStringArray reused input slice, got source %#v", in)
	}
}

func TestIsStringOrStringList(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want bool
	}{
		{"string", "solo", true},
		{"empty string", "", true},
		{"list of strings", []any{"a", ""}, true},
		{"empty list", []any{}, true},
		{"list with non-string", []any{"a", 1}, false},
		{"nil", nil, false},
		{"integer", 1, false},
		{"native string slice", []string{"a"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsStringOrStringList(c.in); got != c.want {
				t.Errorf("IsStringOrStringList(%#v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestIsNonEmptyStringList(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want bool
	}{
		{"string", "solo", true},
		{"empty string", "", false},
		{"list of strings", []any{"a", ""}, true},
		{"empty list", []any{}, false},
		{"list with only empties", []any{"", ""}, false},
		{"list with non-string", []any{"a", 1}, false},
		{"nil", nil, false},
		{"integer", 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsNonEmptyStringList(c.in); got != c.want {
				t.Errorf("IsNonEmptyStringList(%#v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestIsNonEmptyStringArray(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want bool
	}{
		{"list of strings", []any{"a", "b"}, true},
		{"list with empty string", []any{"a", ""}, false},
		{"list with only empty string", []any{""}, false},
		{"empty list", []any{}, false},
		{"list with non-string", []any{"a", 1}, false},
		{"string", "solo", false},
		{"nil", nil, false},
		{"native string slice", []string{"a"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsNonEmptyStringArray(c.in); got != c.want {
				t.Errorf("IsNonEmptyStringArray(%#v) = %v, want %v", c.in, got, c.want)
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
			if got := StringArray(c.in); !slices.Equal(got, c.want) {
				t.Errorf("StringArray(%#v) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

// TestStringArrayBareStringNil pins StringArray !ok -> nil (mutant .19).
func TestStringArrayBareStringNil(t *testing.T) {
	if got := StringArray("solo"); got != nil {
		t.Errorf("StringArray(bare string) = %#v, want nil", got)
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
		{int64(maxInt), maxInt, true}, // int64Value max boundary (mutant .51)
		{int64(minInt), minInt, true}, // int64Value min boundary (mutant .50)
		{uint64(7), 7, true},
		{uint64(maxInt), maxInt, true}, // largest uint64 that still fits int (boundary)
		{uint64(maxInt) + 1, 0, false},
		{8.9, 8, true},   // float truncates
		{10.0, 10, true}, // float64Int ParseInt base (mutant .62)
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
		{"1MiB", 1 << 20, true},
		{"1MB", 1 << 20, true},
		{"1M", 1 << 20, true},
		{"1B", 1, true},
		{"1.5G", 1536 << 20, true},
		{"1GB", 1 << 30, true},
		{"2T", 2 << 40, true},
		{"1TB", 1 << 40, true},
		{"1TiB", 1 << 40, true}, // TiB suffix (mutant .107)
		{"1GiB", 1 << 30, true}, // GiB suffix (mutant .113)
		{"1 GiB", 1 << 30, true},
		{"0", 0, false},
		{"0G", 0, true},
		{"", 0, false},
		{"-1G", 0, false},
		{"NaN", 0, false},
		{"10P", 0, false},
		{"16777215T", 16777215 << 40, true}, // (2^24-1) TiB: largest that still fits uint64
		{"16777216T", 0, false},             // 2^24 TiB == 2^64: must not overflow to a small value
		{"99999999T", 0, false},             // far over 2^64
		{true, 0, false},
	}
	for _, c := range cases {
		got, ok := ByteSize(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ByteSize(%#v) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestPercent(t *testing.T) {
	cases := []struct {
		in   any
		want float64
		ok   bool
	}{
		{"0", 0, true},
		{"100", 100, true},
		{" 10% ", 10, true},
		{"12.5%", 12.5, true},
		{75, 75, true},
		{-1, 0, false},
		{"101", 0, false},
		{"NaN", 0, false},
		{"bad", 0, false},
		{"%", 0, false},
	}
	for _, c := range cases {
		got, ok := Percent(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("Percent(%#v) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
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

func TestIntList(t *testing.T) {
	cases := []struct {
		name   string
		in     any
		want   []int
		wantOK bool
	}{
		{"single int", 1, []int{1}, true},
		{"single string int", "2", []int{2}, true},
		{"list of ints", []any{0, 1}, []int{0, 1}, true},
		{"list of string ints", []any{"0", "1"}, []int{0, 1}, true},
		{"empty list invalid", []any{}, nil, false},
		{"bad scalar invalid", "bad", nil, false},
		{"mixed list invalid", []any{0, "bad"}, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := IntList(c.in)
			if ok != c.wantOK {
				t.Fatalf("IntList(%#v) ok = %v, want %v", c.in, ok, c.wantOK)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("IntList(%#v) = %#v, want %#v", c.in, got, c.want)
			}
		})
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

// TestCompareFloatCoversCompareOps locks the two halves of the operator
// vocabulary together: every op IsCompareOp accepts must be implemented by
// CompareFloat (an accepted-but-unimplemented op would silently never fire),
// and an unknown op must neither validate nor hold.
func TestCompareFloatCoversCompareOps(t *testing.T) {
	for _, op := range []string{">=", ">", "<=", "<", "==", "!="} {
		if !IsCompareOp(op) {
			t.Fatalf("IsCompareOp(%q) = false", op)
		}
		// 2 op 1 and 1 op 2 cannot both be false for an implemented operator.
		if !CompareFloat(2, op, 1) && !CompareFloat(1, op, 2) && !CompareFloat(1, op, 1) {
			t.Fatalf("CompareFloat does not implement %q", op)
		}
	}
	if IsCompareOp("=~") || CompareFloat(1, "=~", 1) {
		t.Fatal("=~ is assertion-only: not a compare op and never holds numerically")
	}
	if CompareFloat(1, "nope", 1) {
		t.Fatal("an unknown op must never hold")
	}
}

// TestCompareFloatExact pins the exact truth value of every operator at the
// three relations (a<b, a==b, a>b). The "covers" test above only proves each op
// fires somewhere; this one nails the boundary (a==b) and polarity of each, so a
// drifted operator (e.g. ">=" weakened to ">", or "==" flipped to "!=") fails.
func TestCompareFloatExact(t *testing.T) {
	cases := []struct {
		a    float64
		op   string
		b    float64
		want bool
	}{
		{1, ">=", 2, false}, {2, ">=", 2, true}, {3, ">=", 2, true},
		{1, ">", 2, false}, {2, ">", 2, false}, {3, ">", 2, true},
		{1, "<=", 2, true}, {2, "<=", 2, true}, {3, "<=", 2, false},
		{1, "<", 2, true}, {2, "<", 2, false}, {3, "<", 2, false},
		{2, "==", 2, true}, {2, "==", 3, false},
		{2, "!=", 2, false}, {2, "!=", 3, true},
		{2, "??", 2, false}, // unknown op never holds
	}
	for _, c := range cases {
		if got := CompareFloat(c.a, c.op, c.b); got != c.want {
			t.Errorf("CompareFloat(%v, %q, %v) = %v, want %v", c.a, c.op, c.b, got, c.want)
		}
	}
}

func TestFloat(t *testing.T) {
	cases := []struct {
		in   any
		want float64
		ok   bool
	}{
		{5, 5, true}, {int64(5), 5, true}, {uint64(5), 5, true}, {2.5, 2.5, true},
		{"3.5", 3.5, true}, {"  7 ", 7, true},
		{"x", 0, false}, {nil, 0, false}, {true, 0, false},
	}
	for _, c := range cases {
		if got, ok := Float(c.in); got != c.want || ok != c.ok {
			t.Errorf("Float(%#v) = %v,%v want %v,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestIsAssertOp(t *testing.T) {
	for _, op := range []string{">=", ">", "<=", "<", "==", "!=", "contains", "=~"} {
		if !IsAssertOp(op) {
			t.Errorf("IsAssertOp(%q) = false, want true", op)
		}
	}
	for _, op := range []string{"", "~", "in", "matches"} {
		if IsAssertOp(op) {
			t.Errorf("IsAssertOp(%q) = true, want false", op)
		}
	}
}

func TestDisabled(t *testing.T) {
	if !Disabled(map[string]any{"enabled": false}) {
		t.Error("enabled:false must read as disabled")
	}
	for _, entry := range []map[string]any{
		{"enabled": true}, {}, {"enabled": "false"}, // non-bool enabled is NOT an opt-out
	} {
		if Disabled(entry) {
			t.Errorf("%v must read as enabled", entry)
		}
	}
}
