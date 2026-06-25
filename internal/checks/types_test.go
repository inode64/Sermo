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

func TestExpectExitText(t *testing.T) {
	// No expected codes defaults to "0"; multiple codes join with " or ".
	if got := ExpectExitText(nil); got != "0" {
		t.Errorf("ExpectExitText(nil) = %q, want 0", got)
	}
	if got := ExpectExitText([]int{2, 3}); got != "2 or 3" {
		t.Errorf("ExpectExitText([2 3]) = %q, want \"2 or 3\"", got)
	}
}
