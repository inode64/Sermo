package checks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
// A validator of an optional file has nothing to validate while the file is
// absent; a present file is the command's to judge, failure included.
func TestCommandCheckIfExistsRunsOnlyWhileThePathExists(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "daemon.json")
	if err := os.WriteFile(present, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := execxtest.Fixed(execx.Result{ExitCode: 1, Stderr: "unable to configure the Docker daemon"}, nil)
	c := commandCheck{name: "config", runner: runner, argv: []string{"/usr/bin/dockerd", "--validate"}, expectExit: []int{0}}

	c.ifExists = filepath.Join(dir, "absent.json")
	res := c.Run(t.Context())
	if !res.OK || !strings.Contains(res.Message, "does not exist") {
		t.Fatalf("absent path: ok=%v message=%q, want a passing result that says so", res.OK, res.Message)
	}
	if calls := runner.Calls(); len(calls) != 0 {
		t.Fatalf("absent path ran the command: %v", calls)
	}

	c.ifExists = present
	if res = c.Run(t.Context()); res.OK {
		t.Fatalf("present path: the failing command passed: %s", res.Message)
	}
	if calls := runner.Calls(); len(calls) != 1 {
		t.Fatalf("present path: command ran %d times, want 1", len(calls))
	}
}

func TestCommandCheckIfExistsRequiresAnAbsoluteCleanPath(t *testing.T) {
	for _, path := range []any{"", "etc/docker/daemon.json", "/etc/docker/../daemon.json", 7} {
		entry := map[string]any{CheckKeyCommand: []any{"/usr/bin/dockerd"}, CheckKeyIfExists: path}
		if err := ValidateCommandIfExists(entry); err == nil {
			t.Errorf("if_exists %v: accepted", path)
		}
	}
	entry := map[string]any{CheckKeyCommand: []any{"/usr/bin/dockerd"}, CheckKeyIfExists: "/etc/docker/daemon.json"}
	if err := ValidateCommandIfExists(entry); err != nil {
		t.Fatalf("valid if_exists rejected: %v", err)
	}
}

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

// A command whose own program cannot load a library is a warning that never
// drives an operation, whatever expect_exit lists: a health check reads it as
// failing, a condition check has no reading. A declared grade still wins, and a
// preflight that runs it still blocks.
func TestCommandCheckProgramMissingALibrary(t *testing.T) {
	const traceback = "Traceback (most recent call last):\n  File \"/usr/bin/ceph\", line 163, in <module>\nModuleNotFoundError: No module named 'prettytable'\n"
	const missing = "ModuleNotFoundError: No module named 'prettytable'"
	tests := []struct {
		name         string
		expectExit   []int
		condition    bool
		reports      string
		declared     severity.Level
		wantSeverity severity.Level
		wantObserved ObservationState
	}{
		{name: "health", expectExit: []int{0}, wantSeverity: severity.Warning, wantObserved: ObservationFailing},
		{name: "exit listed as expected", expectExit: []int{0, 1}, wantSeverity: severity.Warning, wantObserved: ObservationFailing},
		{name: "condition", expectExit: []int{0}, condition: true, reports: ReportsCondition, wantSeverity: severity.Warning, wantObserved: ObservationUnavailable},
		{name: "declared grade", expectExit: []int{0}, declared: severity.Error, wantSeverity: severity.Error, wantObserved: ObservationFailing},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := commandCheck{
				name: "restart-if-status-failed", condition: tc.condition, reports: tc.reports, severity: tc.declared,
				runner:     execxtest.Fixed(execx.Result{ExitCode: 1, Stderr: traceback}, nil),
				argv:       []string{"/usr/bin/ceph", "--admin-daemon", "/run/ceph/ceph-mon.a.asok", "mon_status"},
				expectExit: tc.expectExit,
			}
			res := c.Run(t.Context())
			if res.OK || !res.ActionBlocked || res.Optional {
				t.Fatalf("OK=%t ActionBlocked=%t Optional=%t, want a failed required result that blocks actions", res.OK, res.ActionBlocked, res.Optional)
			}
			if got := res.Observation(); got != tc.wantObserved {
				t.Fatalf("observation = %q, want %q", got, tc.wantObserved)
			}
			if res.Severity != tc.wantSeverity {
				t.Fatalf("severity = %q, want %q", res.Severity, tc.wantSeverity)
			}
			if res.Data[DataKeyMissingLibrary] != missing || res.Data[DataKeyOutput] == nil {
				t.Fatalf("data = %v, want the missing library and the output", res.Data)
			}
			if want := "exit 1; program could not load a library: " + missing; res.Message != want {
				t.Fatalf("message = %q, want %q", res.Message, want)
			}
			if Evaluate([]Result{res}).OK {
				t.Fatal("a preflight running this program must still block")
			}
		})
	}

	// A failure the probed service caused keeps its ordinary verdict.
	c := commandCheck{
		name:       "restart-if-status-failed",
		runner:     execxtest.Fixed(execx.Result{ExitCode: 22, Stderr: "admin_socket: exception getting command descriptions: [Errno 111] Connection refused\n"}, nil),
		argv:       []string{"/usr/bin/ceph", "--admin-daemon", "/run/ceph/ceph-mon.a.asok", "mon_status"},
		expectExit: []int{0},
	}
	if res := c.Run(t.Context()); res.Unavailable || res.ActionBlocked || res.Severity.Advisory() || res.Data[DataKeyMissingLibrary] != nil {
		t.Fatalf("service failure = %+v, want an ordinary failing result", res)
	}
}
