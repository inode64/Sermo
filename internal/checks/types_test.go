package checks

import "testing"

func TestJSONAssertNumericBoundaries(t *testing.T) {
	// got == want exercises each operator's boundary exactly.
	cases := []struct {
		op   string
		want bool
	}{
		{">", false}, // 5 > 5
		{">=", true}, // 5 >= 5
		{"<", false}, // 5 < 5
		{"<=", true}, // 5 <= 5
	}
	for _, c := range cases {
		if got := jsonAssert(5.0, c.op, "5"); got != c.want {
			t.Errorf("jsonAssert(5, %q, 5) = %v, want %v", c.op, got, c.want)
		}
	}
}

func TestStatusMatcherString(t *testing.T) {
	// Operator form renders "op value"; the code/class form is a comma list.
	if got := (statusMatcher{op: ">", value: "500"}).String(); got != "> 500" {
		t.Errorf("op-form String() = %q, want \"> 500\"", got)
	}
	if got := (statusMatcher{codes: []int{200, 404}}).String(); got != "200,404" {
		t.Errorf("code-form String() = %q, want 200,404", got)
	}
}
