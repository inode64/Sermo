package checks

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJSONAssertNumericBoundaries(t *testing.T) {
	// got == want exercises each operator's boundary exactly through compareValue.
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
		got, err := jsonAssert(5.0, c.op, "5")
		if err != nil {
			t.Errorf("jsonAssert(5, %q, 5): %v", c.op, err)
			continue
		}
		if got != c.want {
			t.Errorf("jsonAssert(5, %q, 5) = %v, want %v", c.op, got, c.want)
		}
	}
}

func TestJSONAssertUsesCompareValue(t *testing.T) {
	cases := []struct {
		name    string
		got     any
		op      string
		want    string
		hold    bool
		wantErr bool
	}{
		{name: "json number equals integer spelling", got: 7.0, op: "==", want: "7", hold: true},
		{name: "json number equals float spelling", got: 7.0, op: "==", want: "7.0", hold: true},
		{name: "json number not equal", got: 7.0, op: "!=", want: "8", hold: true},
		{name: "json string equality", got: "ok", op: "==", want: "ok", hold: true},
		{name: "json string contains", got: "role:master", op: "contains", want: "master", hold: true},
		{name: "non-numeric ordering", got: "ready", op: ">", want: "1", wantErr: true},
		{name: "invalid regex", got: "v1", op: "=~", want: "[", wantErr: true},
		{name: "unsupported op", got: 1.0, op: "><", want: "1", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hold, err := jsonAssert(c.got, c.op, c.want)
			if c.wantErr {
				if err == nil {
					t.Fatalf("jsonAssert(%v, %q, %q): expected error", c.got, c.op, c.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("jsonAssert(%v, %q, %q): %v", c.got, c.op, c.want, err)
			}
			if hold != c.hold {
				t.Fatalf("jsonAssert(%v, %q, %q) = %v, want %v", c.got, c.op, c.want, hold, c.hold)
			}
		})
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

// assertPathCheckFailureVsMissing checks that mk(path) reports a wrong-type path
// (built by setupWrong) distinctly from a simply-missing one.
func assertPathCheckFailureVsMissing(t *testing.T, mk func(paths []string) Check, setupWrong func(dir string) (path, wantMsg string)) {
	t.Helper()
	dir := t.TempDir()
	wrongPath, wantMsg := setupWrong(dir)
	if res := mk([]string{wrongPath}).Run(context.Background()); res.OK || !strings.Contains(res.Message, wantMsg) {
		t.Fatalf("wrong-type path: %+v", res)
	}
	if res := mk([]string{filepath.Join(dir, "missing")}).Run(context.Background()); res.OK || !strings.Contains(res.Message, "does not exist") {
		t.Fatalf("missing path: %+v", res)
	}
}

func TestLockfileCheckFailureVsMissing(t *testing.T) {
	// A path that exists but is not a regular file is a failure, reported
	// distinctly from a path that is simply missing.
	assertPathCheckFailureVsMissing(t,
		func(paths []string) Check { return lockfileCheck{name: "l", paths: paths} },
		func(dir string) (string, string) { return dir, "not a regular file" })
}

func TestSocketCheckFailureVsMissing(t *testing.T) {
	// A regular file where a socket is expected is a failure, distinct from missing.
	assertPathCheckFailureVsMissing(t,
		func(paths []string) Check { return socketCheck{name: "s", paths: paths} },
		func(dir string) (string, string) {
			regular := filepath.Join(dir, "not.sock")
			writeFile(t, regular, "x")
			return regular, "is not a socket"
		})
}

func TestLevelCountResultFreeOmittedWhenLimitUnknown(t *testing.T) {
	now := time.Now()
	// limit 0 means the kernel max is unknown: free must be omitted entirely.
	r := levelCountResult(base{name: "x"}, nil, "fds", "fds", "allocated", 5, 0, now)
	if _, has := r.Data["free"]; has {
		t.Fatalf("unknown limit must omit free, got %v", r.Data)
	}
	// A known limit carries free = limit - count.
	r2 := levelCountResult(base{name: "x"}, nil, "fds", "fds", "allocated", 4, 10, now)
	if r2.Data["free"] != uint64(6) {
		t.Fatalf("free = %v, want 6", r2.Data["free"])
	}
}
