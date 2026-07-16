package checks

import (
	"context"
	"testing"
	"time"

	"sermo/internal/execx"
)

func TestParseOutputMatcher(t *testing.T) {
	cases := []struct {
		name      string
		in        any
		wantSub   string
		wantOp    string
		wantVal   string
		wantWarn  bool
		notActive bool
	}{
		{name: "absent", in: nil, notActive: true},
		{name: "substring", in: "OK", wantSub: "OK"},
		{name: "empty substring inactive", in: "", notActive: true},
		{name: "op value", in: map[string]any{"op": ">", "value": 5}, wantOp: ">", wantVal: "5"},
		{name: "invalid op", in: map[string]any{"op": "=>", "value": "1"}, wantWarn: true, notActive: true},
		{name: "invalid numeric value", in: map[string]any{"op": ">", "value": "abc"}, wantWarn: true, notActive: true},
		{name: "invalid regex value", in: map[string]any{"op": "=~", "value": "["}, wantWarn: true, notActive: true},
		{name: "wrong type", in: 42, wantWarn: true, notActive: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, warn := ParseOutputMatcher(c.in)
			if (warn != "") != c.wantWarn {
				t.Fatalf("warn = %q, wantWarn %v", warn, c.wantWarn)
			}
			if m.Substring != c.wantSub || m.Op != c.wantOp || m.Value != c.wantVal {
				t.Errorf("matcher = %+v, want sub=%q op=%q val=%q", m, c.wantSub, c.wantOp, c.wantVal)
			}
			if m.Active() == c.notActive {
				t.Errorf("Active() = %v, want %v", m.Active(), !c.notActive)
			}
		})
	}
}

// matchCase is a table row for runMatchCases.
type matchCase[M interface {
	Match(output string) (bool, string)
}] struct {
	name   string
	m      M
	output string
	want   bool
}

// runMatchCases runs Match over each case and asserts the verdict plus a
// non-empty detail on every failed match.
func runMatchCases[M interface {
	Match(output string) (bool, string)
}](t *testing.T, cases []matchCase[M]) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, detail := c.m.Match(c.output)
			if ok != c.want {
				t.Errorf("Match(%q) = (%v, %q), want %v", c.output, ok, detail, c.want)
			}
			if !ok && detail == "" {
				t.Error("a failed match must return a non-empty detail")
			}
		})
	}
}

func TestOutputMatcherMatch(t *testing.T) {
	runMatchCases(t, []matchCase[OutputMatcher]{
		{"inactive matches anything", OutputMatcher{}, "whatever", true},
		{"substring present", OutputMatcher{Substring: "ready"}, "service ready now", true},
		{"substring absent", OutputMatcher{Substring: "ready"}, "service down", false},
		{"numeric op pass", OutputMatcher{Op: ">", Value: "10"}, " 42 ", true},
		{"numeric op fail", OutputMatcher{Op: ">", Value: "10"}, "3", false},
		{"equality string", OutputMatcher{Op: "==", Value: "done"}, "done", true},
		{"regex pass", OutputMatcher{Op: "=~", Value: "^v[0-9]+"}, "v12 build", true},
		{"regex fail", OutputMatcher{Op: "=~", Value: "^v[0-9]+"}, "broken", false},
		{"non-numeric for ordering op", OutputMatcher{Op: ">", Value: "10"}, "abc", false},
	})
}

func TestValidateAssertionValue(t *testing.T) {
	cases := []struct {
		name    string
		label   string
		op      string
		value   string
		wantErr bool
	}{
		{name: "numeric ordering", label: "expect_body", op: ">", value: "10"},
		{name: "invalid numeric", label: "expect_body", op: ">", value: "abc", wantErr: true},
		{name: "valid regex", label: "expect_json.version", op: "=~", value: `^v[0-9]+`},
		{name: "invalid regex", label: "expect_json.version", op: "=~", value: `[`, wantErr: true},
		{name: "contains does not constrain value", op: "contains", value: "["},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAssertionValue(tc.label, tc.op, tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateAssertionValue() err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestParseVersionMatcher(t *testing.T) {
	cases := []struct {
		name      string
		in        any
		wantWarn  bool
		notActive bool
	}{
		{name: "absent", in: nil, notActive: true},
		{name: "contains", in: map[string]any{"contains": "MariaDB"}},
		{name: "excludes", in: map[string]any{"excludes": "MariaDB"}},
		{name: "regex", in: map[string]any{"regex": `(?i)\bmysql\b`}},
		{name: "bad regex", in: map[string]any{"regex": "["}, wantWarn: true, notActive: true},
		{name: "unknown key", in: map[string]any{"rejects": "MariaDB"}, wantWarn: true, notActive: true},
		{name: "wrong type", in: "MariaDB", wantWarn: true, notActive: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, warn := ParseVersionMatcher(c.in)
			if (warn != "") != c.wantWarn {
				t.Fatalf("warn = %q, wantWarn %v", warn, c.wantWarn)
			}
			if m.Active() == c.notActive {
				t.Errorf("Active() = %v, want %v", m.Active(), !c.notActive)
			}
		})
	}
}

func TestVersionMatcherMatch(t *testing.T) {
	runMatchCases(t, []matchCase[VersionMatcher]{
		{"inactive matches anything", VersionMatcher{}, "", true},
		{"contains passes", VersionMatcher{Contains: []string{"MariaDB"}}, "mysqld Ver 11.8.5-MariaDB", true},
		{"contains fails", VersionMatcher{Contains: []string{"MariaDB"}}, "mysqld Ver 8.0.36", false},
		{"all contains required", VersionMatcher{Contains: []string{"mysqld", "MariaDB"}}, "mysqld Ver 8.0.36", false},
		{"excludes passes", VersionMatcher{Excludes: []string{"MariaDB"}}, "mysqld Ver 8.0.36", true},
		{"excludes fails", VersionMatcher{Excludes: []string{"MariaDB"}}, "mysqld Ver 11.8.5-MariaDB", false},
		{"all excludes checked", VersionMatcher{Excludes: []string{"Oracle", "MariaDB"}}, "mysqld Ver 11.8.5-MariaDB", false},
		{"regex passes", VersionMatcher{Regex: []string{`Ver 8\.`}}, "mysqld Ver 8.0.36", true},
		{"regex fails", VersionMatcher{Regex: []string{`Ver 8\.`}}, "mysqld Ver 11.8.5-MariaDB", false},
		{"all regexes checked", VersionMatcher{Regex: []string{`mysqld`, `MariaDB`}}, "mysqld Ver 8.0.36", false},
		{"empty output fails active matcher", VersionMatcher{Excludes: []string{"MariaDB"}}, "", false},
	})
}

func TestVersionOutput(t *testing.T) {
	if got := VersionOutput("stdout", ""); got != "stdout" {
		t.Errorf("VersionOutput(stdout, empty) = %q, want stdout", got)
	}
	if got := VersionOutput("", "stderr"); got != "stderr" {
		t.Errorf("VersionOutput(empty, stderr) = %q, want stderr", got)
	}
	if got := VersionOutput("stdout", "stderr"); got != "stdout\nstderr" {
		t.Errorf("VersionOutput(stdout, stderr) = %q, want newline join", got)
	}
}

func TestCommandCheckOutputExpectations(t *testing.T) {
	mk := func(res execx.Result, expectExit []int, stdout, stderr OutputMatcher) commandCheck {
		return commandCheck{
			base:       base{name: "c", timeout: time.Second},
			runner:     fakeRunner{res},
			argv:       []string{"x"},
			expectExit: expectExit,
			stdout:     stdout,
			stderr:     stderr,
		}
	}
	t.Run("non-zero exit accepted via expect_exit", func(t *testing.T) {
		c := mk(execx.Result{ExitCode: 3}, []int{3}, OutputMatcher{}, OutputMatcher{})
		if res := c.Run(context.Background()); !res.OK {
			t.Errorf("exit 3 with expect_exit 3 should pass: %s", res.Message)
		}
	})
	t.Run("one of several expected exits passes", func(t *testing.T) {
		c := mk(execx.Result{ExitCode: 1}, []int{0, 1}, OutputMatcher{}, OutputMatcher{})
		if res := c.Run(context.Background()); !res.OK {
			t.Errorf("exit 1 with expect_exit [0,1] should pass: %s", res.Message)
		}
	})
	t.Run("stdout substring must match", func(t *testing.T) {
		c := mk(execx.Result{ExitCode: 0, Stdout: "all good\n"}, []int{0}, OutputMatcher{Substring: "good"}, OutputMatcher{})
		if res := c.Run(context.Background()); !res.OK {
			t.Errorf("matching stdout should pass: %s", res.Message)
		}
		c = mk(execx.Result{ExitCode: 0, Stdout: "nope\n"}, []int{0}, OutputMatcher{Substring: "good"}, OutputMatcher{})
		if res := c.Run(context.Background()); res.OK {
			t.Error("non-matching stdout should fail")
		}
	})
	t.Run("stderr op value must match", func(t *testing.T) {
		c := mk(execx.Result{ExitCode: 0, Stderr: "0\n"}, []int{0}, OutputMatcher{}, OutputMatcher{Op: "==", Value: "0"})
		if res := c.Run(context.Background()); !res.OK {
			t.Errorf("matching stderr should pass: %s", res.Message)
		}
		c = mk(execx.Result{ExitCode: 0, Stderr: "5\n"}, []int{0}, OutputMatcher{}, OutputMatcher{Op: "==", Value: "0"})
		if res := c.Run(context.Background()); res.OK {
			t.Error("non-matching stderr should fail")
		}
	})
	t.Run("version_match excludes compatibility identity", func(t *testing.T) {
		c := mk(execx.Result{ExitCode: 0, Stdout: "mysqld Ver 11.8.5-MariaDB\n"}, []int{0}, OutputMatcher{}, OutputMatcher{})
		c.version = VersionMatcher{Excludes: []string{"MariaDB"}}
		if res := c.Run(context.Background()); res.OK {
			t.Error("MariaDB version output should fail a MySQL excludes matcher")
		}
	})
}
