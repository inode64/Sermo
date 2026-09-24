package checks

import "testing"

func TestCompareValue(t *testing.T) {
	cases := []struct {
		result  string
		op      string
		value   string
		want    bool
		wantErr bool
	}{
		{"role:master connected", "contains", "master", true, false},
		{"role:replica", "contains", "master", false, false},
		{"42", ">", "10", true, false},
		{"42", ">", "100", false, false},
		{"10", ">=", "10", true, false},
		{"5", "<", "10", true, false},
		{"10", "<=", "9", false, false},
		{"7", "==", "7", true, false},
		{"7.0", "==", "7", true, false},
		{"7", "!=", "8", true, false},
		{"ok", "==", "ok", true, false},
		{"ok", "==", "fail", false, false},
		{"ok", "!=", "fail", true, false},
		{"v1.2.3", "=~", `^v[0-9]+\.[0-9]+`, true, false},
		{"nope", "=~", `^v[0-9]+`, false, false},
		{"notnum", ">", "10", false, true},
		{"10", ">", "notnum", false, true},
		{"x", "=~", "[", false, true},
		{"x", "><", "1", false, true},
	}
	for _, c := range cases {
		got, err := newValueMatcher(c.op, c.value).compare(c.result)
		if c.wantErr {
			if err == nil {
				t.Errorf("compareValue(%q, %q, %q): expected error", c.result, c.op, c.value)
			}
			continue
		}
		if err != nil {
			t.Errorf("compareValue(%q, %q, %q): %v", c.result, c.op, c.value, err)
			continue
		}
		if got != c.want {
			t.Errorf("compareValue(%q, %q, %q) = %v, want %v", c.result, c.op, c.value, got, c.want)
		}
	}
}

func TestAssertOpValueRequiresValueBeforeNumericValidation(t *testing.T) {
	_, _, warn := assertOpValue(map[string]any{
		CheckKeyOp:    ">",
		CheckKeyValue: "",
	}, "postgres-query")
	if warn != "postgres-query check requires a value" {
		t.Fatalf("warning = %q", warn)
	}
}

func BenchmarkValueMatcherRegex(b *testing.B) {
	const pattern = `^v[0-9]+\.[0-9]+\.[0-9]+$`
	b.Run("prepared", func(b *testing.B) {
		matcher := newValueMatcher("=~", pattern)
		b.ReportAllocs()
		for b.Loop() {
			if ok, err := matcher.compare("v1.2.3"); !ok || err != nil {
				b.Fatal(ok, err)
			}
		}
	})
	b.Run("compile_per_result", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if ok, err := newValueMatcher("=~", pattern).compare("v1.2.3"); !ok || err != nil {
				b.Fatal(ok, err)
			}
		}
	})
}
