package servicemgr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
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
		{name: "deactivating", result: execx.Result{Stdout: "deactivating\n", ExitCode: 3}, runErr: errors.New("exit 3"), want: StatusUnknown, wantUnit: "nginx.service"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := systemdManager{runner: &execxtest.Runner{Default: tc.result, Err: tc.runErr}}
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

// `systemctl is-active` prints "inactive" for a unit systemd cannot find, so a
// misspelled or absent unit is otherwise reported as a permanent failure that
// looks exactly like a real outage. LoadState is what separates the two.
func TestSystemdManagerStatusSeparatesMissingUnitFromStopped(t *testing.T) {
	const isActive = "systemctl is-active -- nginx.service"
	const loadState = "systemctl show -p LoadState --value -- nginx.service"
	inactive := runnerResult{result: execx.Result{Stdout: "inactive\n", ExitCode: 3}, err: errors.New("exit 3")}

	for _, tc := range []struct {
		name      string
		loadState runnerResult
		want      Status
	}{
		{"missing unit", runnerResult{result: execx.Result{Stdout: "not-found\n"}}, StatusUnknown},
		{"stopped unit", runnerResult{result: execx.Result{Stdout: "loaded\n"}}, StatusInactive},
		{"masked unit stays inactive", runnerResult{result: execx.Result{Stdout: "masked\n"}}, StatusInactive},
		// An unreadable LoadState is not evidence of a missing unit.
		{"unreadable load state", runnerResult{result: execx.Result{ExitCode: -1}, err: errors.New("boom")}, StatusInactive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := multiResultRunner(map[string]runnerResult{isActive: inactive, loadState: tc.loadState})
			got, err := systemdManager{runner: runner}.Status(context.Background(), "nginx")
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if got.Status != tc.want {
				t.Errorf("Status = %q, want %q", got.Status, tc.want)
			}
		})
	}
}

// An active reading must not pay for the LoadState confirmation: the daemon
// queries every service every cycle and most services are running.
func TestSystemdManagerStatusSkipsLoadStateWhenActive(t *testing.T) {
	runner := multiResultRunner(map[string]runnerResult{
		"systemctl is-active -- nginx.service": {result: execx.Result{Stdout: "active\n"}},
	})
	if _, err := (systemdManager{runner: runner}).Status(context.Background(), "nginx"); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(runner.Lines()) != 1 {
		t.Fatalf("calls = %v, want only the is-active query", runner.Lines())
	}
}

func TestSystemdManagerStatusLaunchFailure(t *testing.T) {
	m := systemdManager{runner: &execxtest.Runner{Default: execx.Result{ExitCode: -1}, Err: errors.New("not found")}}
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
			m := openrcManager{runner: &execxtest.Runner{Default: tc.result}}
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
	runner := multiResultRunner(map[string]runnerResult{
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
	})

	m := openrcManager{runner: runner}
	got, err := m.Status(context.Background(), "firehol")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if got.Status != StatusActive {
		t.Fatalf("Status = %q, want %q", got.Status, StatusActive)
	}
	if calls := strings.Join(runner.Lines(), ","); calls != "rc-service firehol status,rc-status -a" {
		t.Fatalf("calls = %v", runner.Lines())
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
	rec := &execxtest.Runner{}
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

	// Isolated by default: a start or stop must not propagate through the
	// dependency graph, so restarting one service cannot restart others.
	want := []string{
		"systemctl start --job-mode=ignore-dependencies -- nginx.service",
		"systemctl stop --job-mode=ignore-dependencies -- nginx.service",
		"systemctl restart --job-mode=ignore-dependencies -- nginx.service",
	}
	if len(rec.Lines()) != len(want) {
		t.Fatalf("calls = %v, want %v", rec.Lines(), want)
	}
	for i := range want {
		if rec.Lines()[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, rec.Lines()[i], want[i])
		}
	}
}

func TestSystemdManagerActionFailureUsesStderr(t *testing.T) {
	m := systemdManager{runner: &execxtest.Runner{
		Default: execx.Result{Stderr: "Unit nginx.service not found.\n", ExitCode: 5},
		Err:     errors.New("exit 5"),
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
	m := systemdManager{runner: &execxtest.Runner{
		Default: execx.Result{ExitCode: -1, Duration: 2 * time.Second},
		Err:     fmt.Errorf("run systemctl: timeout after 2s: %w", context.DeadlineExceeded),
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
	rec := &execxtest.Runner{}
	m := openrcManager{runner: rec}
	if err := m.Restart(context.Background(), "nginx"); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	// Isolated by default; rc-service takes its options before the service name.
	if len(rec.Lines()) != 1 || rec.Lines()[0] != "rc-service --nodeps nginx restart" {
		t.Fatalf("calls = %v, want [rc-service --nodeps nginx restart]", rec.Lines())
	}
}

// A service that opts back in gets the plain command, so the init system
// handles its dependencies as before.
func TestManagerActionsAllowDependenciesWhenOptedIn(t *testing.T) {
	ctx := context.Background()

	sysRec := &execxtest.Runner{}
	sysMgr := systemdManager{runner: sysRec, opts: Options{AllowDependencies: true}}
	if err := sysMgr.Restart(ctx, "nginx"); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if len(sysRec.Lines()) != 1 || sysRec.Lines()[0] != "systemctl restart -- nginx.service" {
		t.Fatalf("systemd calls = %v, want the plain restart", sysRec.Lines())
	}

	rcRec := &execxtest.Runner{}
	rcMgr := openrcManager{runner: rcRec, opts: Options{AllowDependencies: true}}
	if err := rcMgr.Restart(ctx, "nginx"); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if len(rcRec.Lines()) != 1 || rcRec.Lines()[0] != "rc-service nginx restart" {
		t.Fatalf("openrc calls = %v, want the plain restart", rcRec.Lines())
	}
}

// Only state-changing verbs propagate; querying, reloading and clearing failed
// state must stay untouched so the flag cannot alter unrelated commands.
func TestManagerNonStateVerbsCarryNoIsolationFlag(t *testing.T) {
	ctx := context.Background()

	sysRec := &execxtest.Runner{}
	sysMgr := systemdManager{runner: sysRec}
	if err := sysMgr.Reload(ctx, "nginx"); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if err := sysMgr.ResetState(ctx, "nginx"); err != nil {
		t.Fatalf("ResetState() error = %v", err)
	}
	want := []string{
		"systemctl reload -- nginx.service",
		"systemctl reset-failed -- nginx.service",
	}
	for i := range want {
		if sysRec.Lines()[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, sysRec.Lines()[i], want[i])
		}
	}
}

func TestResetStateReconcilesInitState(t *testing.T) {
	sysRec := &execxtest.Runner{}
	if err := (systemdManager{runner: sysRec}).ResetState(context.Background(), "nginx"); err != nil {
		t.Fatalf("systemd ResetState() error = %v", err)
	}
	if len(sysRec.Lines()) != 1 || sysRec.Lines()[0] != "systemctl reset-failed -- nginx.service" {
		t.Fatalf("systemd calls = %v, want [systemctl reset-failed -- nginx.service]", sysRec.Lines())
	}

	rcRec := &execxtest.Runner{}
	if err := (openrcManager{runner: rcRec}).ResetState(context.Background(), "nginx"); err != nil {
		t.Fatalf("openrc ResetState() error = %v", err)
	}
	if len(rcRec.Lines()) != 1 || rcRec.Lines()[0] != "rc-service nginx zap" {
		t.Fatalf("openrc calls = %v, want [rc-service nginx zap]", rcRec.Lines())
	}
}

func TestNewManagerUnsupportedBackend(t *testing.T) {
	if _, err := newManager(BackendAuto, &execxtest.Runner{}, Options{}); err == nil {
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
		m := systemdManager{runner: &execxtest.Runner{Default: execx.Result{Stdout: tc.stdout}}}
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

type runnerResult struct {
	result execx.Result
	err    error
}

func TestActionErrorPrefersRunErrorOnLaunchFailure(t *testing.T) {
	// ExitCode -1 with a runner error is a launch failure: the message comes from
	// the run error, not from stale stderr of a process that never ran.
	err := actionError("systemctl start x", execx.Result{ExitCode: -1, Stderr: "stderr-msg"}, errors.New("boom"))
	if err == nil || !strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "stderr-msg") {
		t.Fatalf("actionError = %v, want it to surface the run error 'boom', not stderr", err)
	}
}

// Empty stdout with a zero exit is not a launch failure (only ExitCode < 0 is),
// so Status/SupportsReload must not return a query error across managers.
func TestManagerEmptyZeroExitNotError(t *testing.T) {
	systemd := systemdManager{runner: &execxtest.Runner{Default: execx.Result{Stdout: "", ExitCode: 0}}}
	openrc := openrcManager{runner: &execxtest.Runner{Default: execx.Result{Stdout: "", ExitCode: 0}}}
	cases := []struct {
		name string
		call func() error
	}{
		{"systemd Status", func() error { _, err := systemd.Status(context.Background(), "nginx"); return err }},
		{"systemd SupportsReload", func() error { _, err := systemd.SupportsReload(context.Background(), "nginx"); return err }},
		{"openrc Status", func() error { _, err := openrc.Status(context.Background(), "nginx"); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.call(); err != nil {
				t.Fatalf("empty output with exit 0 must not error: %v", err)
			}
		})
	}
}

// OpenRC exits non-zero when a starting service is held in `inactive` awaiting
// its readiness callback (openvpn until the tunnel is up). That is a running
// daemon, not a failure: the action succeeds and status verification owns
// convergence. Any other non-zero start still errors.
func TestOpenrcStartAcceptsStartedButInactive(t *testing.T) {
	inactive := &execxtest.Runner{
		Default: execx.Result{ExitCode: 1, Stderr: " * WARNING: openvpn.tun1 has started, but is inactive"},
		Err:     errors.New("exit status 1"),
	}
	m := openrcManager{runner: inactive}
	if err := m.Start(context.Background(), "openvpn.tun1"); err != nil {
		t.Fatalf("started-but-inactive must be a successful submission, got %v", err)
	}
	failed := &execxtest.Runner{
		Default: execx.Result{ExitCode: 1, Stderr: " * ERROR: openvpn.tun1 failed to start"},
		Err:     errors.New("exit status 1"),
	}
	m = openrcManager{runner: failed}
	if err := m.Start(context.Background(), "openvpn.tun1"); err == nil {
		t.Fatal("a genuinely failed start must still error")
	}
}

// multiResultRunner answers exact command lines with the scripted result and
// error; unknown commands succeed with no output.
func multiResultRunner(results map[string]runnerResult) *execxtest.Runner {
	byLine := make(map[string]execx.Result, len(results))
	errs := make(map[string]error, len(results))
	for line, res := range results {
		byLine[line] = res.result
		if res.err != nil {
			errs[line] = res.err
		}
	}
	return &execxtest.Runner{ByLine: byLine, Errs: errs}
}
