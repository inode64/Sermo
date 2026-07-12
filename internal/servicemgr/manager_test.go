package servicemgr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
)

func TestSystemdUnitNormalization(t *testing.T) {
	cases := map[string]string{
		"nginx":         "nginx.service",
		"nginx.service": "nginx.service",
		"sshd.socket":   "sshd.socket",
		"backup.timer":  "backup.timer",
	}
	for input, want := range cases {
		if got := systemdUnit(input); got != want {
			t.Errorf("systemdUnit(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSystemdManagerStatus(t *testing.T) {
	cases := []struct {
		name     string
		result   execx.Result
		runErr   error
		want     Status
		wantUnit string
	}{
		{name: "active", result: execx.Result{Stdout: "active\n"}, want: StatusActive, wantUnit: "nginx.service"},
		{name: "inactive", result: execx.Result{Stdout: "inactive\n", ExitCode: 3}, runErr: errors.New("exit 3"), want: StatusInactive, wantUnit: "nginx.service"},
		{name: "failed", result: execx.Result{Stdout: "failed\n", ExitCode: 3}, runErr: errors.New("exit 3"), want: StatusFailed, wantUnit: "nginx.service"},
		{name: "activating", result: execx.Result{Stdout: "activating\n", ExitCode: 3}, runErr: errors.New("exit 3"), want: StatusUnknown, wantUnit: "nginx.service"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := systemdManager{runner: stubRunner{result: tc.result, err: tc.runErr}}
			got, err := m.Status(context.Background(), "nginx")
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if got.Status != tc.want {
				t.Errorf("Status = %q, want %q", got.Status, tc.want)
			}
			if got.Unit != tc.wantUnit {
				t.Errorf("Unit = %q, want %q", got.Unit, tc.wantUnit)
			}
			if got.Backend != BackendSystemd {
				t.Errorf("Backend = %q, want systemd", got.Backend)
			}
		})
	}
}

func TestSystemdManagerStatusLaunchFailure(t *testing.T) {
	m := systemdManager{runner: stubRunner{result: execx.Result{ExitCode: -1}, err: errors.New("not found")}}
	if _, err := m.Status(context.Background(), "nginx"); err == nil {
		t.Fatal("Status() error = nil, want launch failure")
	}
}

func TestOpenRCManagerStatus(t *testing.T) {
	cases := []struct {
		name   string
		result execx.Result
		want   Status
	}{
		{name: "started stdout", result: execx.Result{Stdout: " * status: started\n"}, want: StatusActive},
		{name: "stopped stdout", result: execx.Result{Stdout: " * status: stopped\n", ExitCode: 3}, want: StatusInactive},
		{name: "not started stdout", result: execx.Result{Stdout: " * status: not started\n", ExitCode: 3}, want: StatusInactive},
		{name: "crashed stdout", result: execx.Result{Stdout: " * status: crashed\n", ExitCode: 1}, want: StatusFailed},
		{name: "exit code fallback active", result: execx.Result{ExitCode: 0}, want: StatusActive},
		{name: "exit code fallback inactive", result: execx.Result{ExitCode: 3}, want: StatusInactive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := openrcManager{runner: stubRunner{result: tc.result}}
			got, err := m.Status(context.Background(), "nginx")
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if got.Status != tc.want {
				t.Errorf("Status = %q, want %q", got.Status, tc.want)
			}
			if got.Unit != "nginx" {
				t.Errorf("Unit = %q, want nginx", got.Unit)
			}
		})
	}
}

func TestOpenRCManagerStatusFallsBackToRCStatus(t *testing.T) {
	runner := &multiResultRunner{results: map[string]runnerResult{
		"rc-service firehol status": {
			result: execx.Result{
				Stdout:   " * Showing FireHOL status ...\n'unknown': I need something more specific.\n",
				ExitCode: 1,
			},
			err: errors.New("exit 1"),
		},
		"rc-status -a": {
			result: execx.Result{Stdout: " sshd [  started  ]\n firehol [  started  ]\n"},
		},
	}}

	m := openrcManager{runner: runner}
	got, err := m.Status(context.Background(), "firehol")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Status != StatusActive {
		t.Fatalf("Status = %q, want %q", got.Status, StatusActive)
	}
	if calls := strings.Join(runner.calls, ","); calls != "rc-service firehol status,rc-status -a" {
		t.Fatalf("calls = %v", runner.calls)
	}
}

func TestOpenRCStatusLineMatchesExactService(t *testing.T) {
	out := "firehol-extra [  started  ]\nfirehol [  stopped  ]\n"
	got, ok := openrcStatusLine(out, "firehol")
	if !ok {
		t.Fatal("openrcStatusLine ok = false")
	}
	if got != StatusInactive {
		t.Fatalf("status = %q, want %q", got, StatusInactive)
	}
}

func TestSystemdManagerActionsUseRunner(t *testing.T) {
	rec := &recordRunner{}
	m := systemdManager{runner: rec}
	ctx := context.Background()

	if err := m.Start(ctx, "nginx"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := m.Stop(ctx, "nginx"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := m.Restart(ctx, "nginx"); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	want := []string{
		"systemctl start -- nginx.service",
		"systemctl stop -- nginx.service",
		"systemctl restart -- nginx.service",
	}
	if len(rec.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", rec.calls, want)
	}
	for i := range want {
		if rec.calls[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, rec.calls[i], want[i])
		}
	}
}

func TestSystemdManagerActionFailureUsesStderr(t *testing.T) {
	m := systemdManager{runner: stubRunner{
		result: execx.Result{Stderr: "Unit nginx.service not found.\n", ExitCode: 5},
		err:    errors.New("exit 5"),
	}}
	err := m.Start(context.Background(), "nginx")
	if err == nil {
		t.Fatal("Start() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want stderr detail", err)
	}
}

func TestSystemdManagerActionTimeoutMessage(t *testing.T) {
	m := systemdManager{runner: stubRunner{
		result: execx.Result{ExitCode: -1, Duration: 2 * time.Second},
		err:    fmt.Errorf("run systemctl: timeout after 2s: %w", context.DeadlineExceeded),
	}}
	err := m.Start(context.Background(), "nginx")
	if err == nil {
		t.Fatal("Start() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "timeout after 2s") {
		t.Fatalf("error = %v, want timeout after duration", err)
	}
	if strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("error = %v, want operator-facing timeout without raw context error", err)
	}
}

func TestOpenRCManagerActionUsesRunner(t *testing.T) {
	rec := &recordRunner{}
	m := openrcManager{runner: rec}
	if err := m.Restart(context.Background(), "nginx"); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if len(rec.calls) != 1 || rec.calls[0] != "rc-service nginx restart" {
		t.Fatalf("calls = %v, want [rc-service nginx restart]", rec.calls)
	}
}

func TestResetStateReconcilesInitState(t *testing.T) {
	sysRec := &recordRunner{}
	if err := (systemdManager{runner: sysRec}).ResetState(context.Background(), "nginx"); err != nil {
		t.Fatalf("systemd ResetState() error = %v", err)
	}
	if len(sysRec.calls) != 1 || sysRec.calls[0] != "systemctl reset-failed -- nginx.service" {
		t.Fatalf("systemd calls = %v, want [systemctl reset-failed -- nginx.service]", sysRec.calls)
	}

	rcRec := &recordRunner{}
	if err := (openrcManager{runner: rcRec}).ResetState(context.Background(), "nginx"); err != nil {
		t.Fatalf("openrc ResetState() error = %v", err)
	}
	if len(rcRec.calls) != 1 || rcRec.calls[0] != "rc-service nginx zap" {
		t.Fatalf("openrc calls = %v, want [rc-service nginx zap]", rcRec.calls)
	}
}

func TestNewManagerUnsupportedBackend(t *testing.T) {
	if _, err := newManager(BackendAuto, stubRunner{}); err == nil {
		t.Fatal("newManager(auto) error = nil, want unsupported error")
	}
}

func TestSystemdManagerSupportsReload(t *testing.T) {
	cases := []struct {
		stdout string
		want   bool
	}{
		{"yes\n", true},
		{"no\n", false},
		{"", false},
	}
	for _, tc := range cases {
		m := systemdManager{runner: stubRunner{result: execx.Result{Stdout: tc.stdout}}}
		got, err := m.SupportsReload(context.Background(), "nginx")
		if err != nil {
			t.Fatalf("SupportsReload(%q): %v", tc.stdout, err)
		}
		if got != tc.want {
			t.Errorf("CanReload=%q -> SupportsReload=%v, want %v", tc.stdout, got, tc.want)
		}
	}
}

func TestOpenrcManagerSupportsReload(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   bool
	}{
		{"reload func", "#!/sbin/openrc-run\nreload() {\n\tstart-stop-daemon --signal HUP\n}\n", true},
		{"reload func with space", "#!/sbin/openrc-run\nreload () {\n\t:\n}\n", true},
		{"extra_started_commands", "extra_started_commands=\"reload\"\n", true},
		{"extra_commands with others", "extra_commands=\"checkconfig reload\"\n", true},
		{"description_reload", "description_reload=\"reload config\"\n", true},
		{"no reload", "#!/sbin/openrc-run\nstart() { :; }\n", false},
		{"commented out", "#!/sbin/openrc-run\n# extra_commands=\"reload\"\n", false},
		{"forcereload substring", "extra_commands=\"forcereload\"\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := openrcManager{readFile: func(string) ([]byte, error) { return []byte(tc.script), nil }}
			got, err := m.SupportsReload(context.Background(), "svc")
			if err != nil {
				t.Fatalf("SupportsReload: %v", err)
			}
			if got != tc.want {
				t.Errorf("script %q -> %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestOpenrcManagerSupportsReloadUnreadableScript(t *testing.T) {
	m := openrcManager{readFile: func(string) ([]byte, error) { return nil, errors.New("no such file") }}
	got, err := m.SupportsReload(context.Background(), "svc")
	if err != nil {
		t.Fatalf("unreadable script must not error: %v", err)
	}
	if got {
		t.Error("an unreadable init script must report reload unsupported (false)")
	}
}

type stubRunner struct {
	result execx.Result
	err    error
}

func (r stubRunner) Run(context.Context, string, ...string) (execx.Result, error) {
	return r.result, r.err
}

type recordRunner struct {
	calls []string
}

func (r *recordRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	var call strings.Builder
	call.WriteString(name)
	for _, arg := range args {
		call.WriteString(" " + arg)
	}
	r.calls = append(r.calls, call.String())
	return execx.Result{}, nil
}

type runnerResult struct {
	result execx.Result
	err    error
}

type multiResultRunner struct {
	results map[string]runnerResult
	calls   []string
}

func (r *multiResultRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	var call strings.Builder
	call.WriteString(name)
	for _, arg := range args {
		call.WriteString(" " + arg)
	}
	r.calls = append(r.calls, call.String())
	res := r.results[call.String()]
	return res.result, res.err
}

func TestActionErrorPrefersRunErrorOnLaunchFailure(t *testing.T) {
	// ExitCode -1 with a runner error is a launch failure: the message comes from
	// the run error, not from stale stderr of a process that never ran.
	err := actionError("systemctl start x", execx.Result{ExitCode: -1, Stderr: "stderr-msg"}, errors.New("boom"))
	if err == nil || !strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "stderr-msg") {
		t.Fatalf("actionError = %v, want it to surface the run error 'boom', not stderr", err)
	}
}

func TestSystemdManagerStatusEmptyZeroExitNotError(t *testing.T) {
	// Empty stdout with a zero exit is not a launch failure (only ExitCode < 0 is),
	// so Status must not return a query error.
	m := systemdManager{runner: stubRunner{result: execx.Result{Stdout: "", ExitCode: 0}}}
	if _, err := m.Status(context.Background(), "nginx"); err != nil {
		t.Fatalf("Status with empty output and exit 0 must not error: %v", err)
	}
}

func TestSystemdSupportsReloadZeroExitNotError(t *testing.T) {
	// Empty output with a zero exit is not a query failure.
	m := systemdManager{runner: stubRunner{result: execx.Result{Stdout: "", ExitCode: 0}}}
	if _, err := m.SupportsReload(context.Background(), "nginx"); err != nil {
		t.Fatalf("SupportsReload with empty output exit 0 must not error: %v", err)
	}
}

func TestOpenRCManagerStatusZeroExitNotError(t *testing.T) {
	// Empty output with a zero exit is not a query failure for OpenRC either.
	m := openrcManager{runner: stubRunner{result: execx.Result{Stdout: "", ExitCode: 0}}}
	if _, err := m.Status(context.Background(), "nginx"); err != nil {
		t.Fatalf("OpenRC Status with empty output exit 0 must not error: %v", err)
	}
}
