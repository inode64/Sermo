package checks

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestBoundTail(t *testing.T) {
	// Exactly the line limit (40) is kept whole; one more line truncates.
	forty := strings.Repeat("x\n", 39) + "x"
	if strings.HasPrefix(boundTail(forty), "… (truncated)") {
		t.Errorf("exactly %d lines must not be truncated", boundedOutputMaxLines)
	}
	fortyOne := strings.Repeat("x\n", 40) + "x"
	if !strings.HasPrefix(boundTail(fortyOne), "… (truncated)") {
		t.Errorf("%d lines must be truncated", boundedOutputMaxLines+1)
	}
	// Exactly the byte limit (single line) is kept whole.
	exact := strings.Repeat("a", boundedOutputMaxBytes)
	if strings.HasPrefix(boundTail(exact), "… (truncated)") {
		t.Errorf("a %d-byte single line must not be truncated", boundedOutputMaxBytes)
	}
}

func TestParseLdSoConf(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ld.so.conf")
	writeFile(t, path, "/opt/lib\n# a comment\n; also a comment\n\ninclude /etc/foo.conf\n/usr/local/lib\n")
	got := parseLdSoConf(path)
	if len(got) != 2 || got[0] != "/opt/lib" || got[1] != "/usr/local/lib" {
		t.Fatalf("parseLdSoConf = %v, want [/opt/lib /usr/local/lib]", got)
	}
	// An unreadable path yields no directories rather than erroring.
	if got := parseLdSoConf(filepath.Join(dir, "missing")); got != nil {
		t.Fatalf("missing file = %v, want nil", got)
	}
}

func TestLockfileCheckFailureVsMissing(t *testing.T) {
	dir := t.TempDir()
	// A path that exists but is not a regular file is a failure, reported
	// distinctly from a path that is simply missing.
	res := lockfileCheck{base: base{name: "l"}, paths: []string{dir}}.Run(context.Background())
	if res.OK || !strings.Contains(res.Message, "not a regular file") {
		t.Fatalf("directory path: %+v", res)
	}
	res2 := lockfileCheck{base: base{name: "l"}, paths: []string{filepath.Join(dir, "missing")}}.Run(context.Background())
	if res2.OK || !strings.Contains(res2.Message, "does not exist") {
		t.Fatalf("missing path: %+v", res2)
	}
}
