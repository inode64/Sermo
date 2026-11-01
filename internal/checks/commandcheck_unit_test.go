package checks

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
	"sermo/internal/severity"
)

// A command check with a unit publishes its first numeric token as the value
// series, quotes only the first stdout line in the message, and records no
// sample (never a failure) when the output has no leading number.
func TestCommandCheckUnitPublishesNumericValue(t *testing.T) {
	c := commandCheck{
		name:       "queue",
		runner:     execxtest.Fixed(execx.Result{ExitCode: 0, Stdout: "17\nnoise line two\n"}, nil),
		argv:       []string{"/bin/exim", "-bpc"},
		expectExit: []int{0},
		numeric:    true,
	}
	res := c.Run(t.Context())
	if !res.OK {
		t.Fatalf("check failed: %s", res.Message)
	}
	if res.Data[DataKeyValue] != 17.0 {
		t.Fatalf("value = %v, want 17", res.Data[DataKeyValue])
	}
	if strings.Contains(res.Message, "noise") || !strings.HasPrefix(res.Message, "17: ") {
		t.Fatalf("message must quote only the first line: %q", res.Message)
	}

	c.runner = execxtest.Fixed(execx.Result{ExitCode: 0, Stdout: "not a number"}, nil)
	res = c.Run(t.Context())
	if !res.OK {
		t.Fatalf("non-numeric output must not fail the check: %s", res.Message)
	}
	if _, present := res.Data[DataKeyValue]; present {
		t.Fatal("non-numeric output must publish no value sample")
	}
}

// A failing command's message states its cause. Apache's configtest reports
// the file and line on one line and the rejected directive on the next; both
// belong in the message, and a command that reports on stdout is heard too.
func TestCommandCheckFailureStatesTheCause(t *testing.T) {
	apache := commandCheck{
		name:       "configuration",
		runner:     execxtest.Fixed(execx.Result{ExitCode: 1, Stderr: "AH00526: Syntax error on line 3 of /etc/apache2/vhosts.d/shop.conf:\nInvalid command 'Foo', perhaps misspelled\nAction 'configtest' failed.\n"}, nil),
		argv:       []string{"/usr/sbin/apache2ctl", "configtest"},
		expectExit: []int{0},
	}
	res := apache.Run(t.Context())
	want := "exit 1 (want 0): AH00526: Syntax error on line 3 of /etc/apache2/vhosts.d/shop.conf: Invalid command 'Foo', perhaps misspelled"
	if res.Message != want {
		t.Fatalf("message = %q, want %q", res.Message, want)
	}
	stdoutOnly := apache
	stdoutOnly.runner = execxtest.Fixed(execx.Result{ExitCode: 2, Stdout: "config invalid: missing key\n"}, nil)
	if res := stdoutOnly.Run(t.Context()); !strings.HasSuffix(res.Message, ": config invalid: missing key") {
		t.Fatalf("stdout cause missing: %q", res.Message)
	}
}

// A declared severity grades the check's failures; it may lower an advisory
// pattern match but never raises a deprecation notice into an outage.
func TestCommandCheckDeclaredSeverityNeverRaisesAnAdvisoryPattern(t *testing.T) {
	c := commandCheck{
		name:       "configuration",
		runner:     execxtest.Fixed(execx.Result{ExitCode: 0, Stderr: "[warn] directive X is deprecated\n"}, nil),
		argv:       []string{"/usr/sbin/apache2ctl", "configtest"},
		expectExit: []int{0},
		analyzer:   mustAnalyzer(t, []any{rule("web-deprecated", "(?i)deprecated", "warning"), rule("syntax", "(?i)syntax error", "error")}),
		severity:   severity.Error,
	}
	if res := c.Run(t.Context()); res.Severity != severity.Warning || !res.Optional {
		t.Fatalf("deprecation under a declared error = %q optional %v, want an advisory warning", res.Severity, res.Optional)
	}
	c.runner = execxtest.Fixed(execx.Result{ExitCode: 0, Stderr: "Syntax error somewhere\n"}, nil)
	if res := c.Run(t.Context()); res.Severity != severity.Error {
		t.Fatalf("syntax error under a declared error = %q, want error", res.Severity)
	}
	c.severity = severity.Info
	c.runner = execxtest.Fixed(execx.Result{ExitCode: 0, Stderr: "[warn] directive X is deprecated\n"}, nil)
	if res := c.Run(t.Context()); res.Severity != severity.Info {
		t.Fatalf("a declared info must still lower a warning pattern, got %q", res.Severity)
	}
}

// A health command a signal killed ran and did not exit with expect_exit, so it
// reads as failing. Unavailable stays set whatever the polarity: rules and
// guards must never read the crash as a false condition.
func TestCommandCheckKilledBySignal(t *testing.T) {
	killed := fmt.Errorf("run /usr/local/bin/app-health: %w 11 (segmentation fault)", execx.ErrKilledBySignal)
	tests := []struct {
		name            string
		condition       bool
		reports         string
		err             error
		wantObservation ObservationState
		wantMessage     string
	}{
		{name: "health reads failing", err: killed, wantObservation: ObservationFailing, wantMessage: "killed by signal 11 (segmentation fault)"},
		{name: "condition has no reading", condition: true, reports: ReportsCondition, err: killed, wantObservation: ObservationUnavailable, wantMessage: "killed by signal 11 (segmentation fault)"},
		{name: "verdictless has no reading", reports: ReportsValue, err: killed, wantObservation: ObservationUnavailable, wantMessage: "killed by signal 11 (segmentation fault)"},
		{name: "a command that never ran has no reading", err: errors.New("run /usr/local/bin/app-health: executable file not found"), wantObservation: ObservationUnavailable, wantMessage: "executable file not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check := commandCheck{
				name: "app", condition: tc.condition, reports: tc.reports,
				runner: execxtest.Fixed(execx.Result{ExitCode: execx.ExitCodeRunFailure}, tc.err),
				argv:   []string{"/usr/local/bin/app-health"},
			}
			res := check.Run(t.Context())
			if res.OK || !res.Unavailable {
				t.Fatalf("OK=%t Unavailable=%t, want a failed result still marked unavailable", res.OK, res.Unavailable)
			}
			if got := res.Observation(); got != tc.wantObservation {
				t.Fatalf("observation = %q, want %q", got, tc.wantObservation)
			}
			if !strings.HasSuffix(res.Message, tc.wantMessage) {
				t.Fatalf("message = %q, want suffix %q", res.Message, tc.wantMessage)
			}
		})
	}
}
