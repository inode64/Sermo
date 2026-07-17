package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/servicemgr"
)

func TestVersionCommand(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-V", "--json version"} {
		var stdout bytes.Buffer
		app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
		if code := app.Run(context.Background(), strings.Fields(arg)); code != exitSuccess {
			t.Fatalf("Run(%q) exit = %d, want %d", arg, code, exitSuccess)
		}
		if !strings.HasPrefix(stdout.String(), "sermo ") {
			t.Errorf("Run(%q) stdout = %q, want it to start with %q", arg, stdout.String(), "sermo ")
		}
	}
}

func TestVersionFlagNotTriggeredAsFlagValue(t *testing.T) {
	// `-V` here is the value of --reason, not a version request, so it must not
	// set opts.version (the old all-args scan would have matched it).
	opts, err := parseArgs([]string{"lock", "acquire", "svc", "--reason", "-V", "--ttl", "1h"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if opts.version {
		t.Fatal("a -V flag value must not request the version")
	}
	if opts.reason != "-V" {
		t.Fatalf("reason = %q, want -V (the flag value)", opts.reason)
	}

	// As a standalone global flag it does request the version.
	if v, err := parseArgs([]string{"--version"}); err != nil || !v.version {
		t.Fatalf("parseArgs(--version) = %+v, %v; want version=true", v, err)
	}
}

func TestHelpTopics(t *testing.T) {
	for _, tc := range []struct {
		name         string
		args         []string
		wantExit     int
		useStderr    bool
		wantContains []string
		wantNoPrefix string
	}{
		{
			name:     "root usage",
			args:     []string{"--help"},
			wantExit: exitSuccess,
			wantContains: []string{
				"Sermo operator CLI",
				"Usage:",
				"Global Flags:",
				"Safe Service Operations:",
				"sermoctl help [COMMAND]",
				"Use `sermoctl help COMMAND`",
			},
		},
		{
			name:     "command topic",
			args:     []string{"help", "restart"},
			wantExit: exitSuccess,
			wantContains: []string{
				"Command: sermoctl restart",
				"sermoctl restart SERVICE [--no-cascade]",
				"--no-cascade",
				"Manual restarts are not remediation-rate-limited",
			},
		},
		{
			name:         "version topic omits banner",
			args:         []string{"help", "version"},
			wantExit:     exitSuccess,
			wantContains: []string{"Command: sermoctl version"},
			wantNoPrefix: "sermo ",
		},
		{
			name:         "command --help flag",
			args:         []string{"status", "--help"},
			wantExit:     exitSuccess,
			wantContains: []string{"Command: sermoctl status", "sermoctl status SERVICE"},
		},
		{
			name:         "unknown topic",
			args:         []string{"help", "not-a-command"},
			wantExit:     exitUsage,
			useStderr:    true,
			wantContains: []string{`unknown help topic "not-a-command"`, "Command: sermoctl help"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &stderr}

			code := app.Run(context.Background(), tc.args)
			if code != tc.wantExit {
				t.Fatalf("%v exit = %d, want %d; stderr=%s", tc.args, code, tc.wantExit, stderr.String())
			}
			out := stdout.String()
			if tc.useStderr {
				out = stderr.String()
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(out, want) {
					t.Fatalf("%v output missing %q:\n%s", tc.args, want, out)
				}
			}
			if tc.wantNoPrefix != "" && strings.HasPrefix(out, tc.wantNoPrefix) {
				t.Fatalf("%v output must not start with %q:\n%s", tc.args, tc.wantNoPrefix, out)
			}
		})
	}
}

func TestBackendCommandPrintsDetectedBackend(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := App{
		Detector: fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		Env:      func(string) string { return "" },
		Stdout:   &stdout,
		Stderr:   &stderr,
	}

	code := app.Run(context.Background(), []string{"backend"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d; stderr=%s", code, exitSuccess, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "systemd" {
		t.Fatalf("stdout = %q, want systemd", got)
	}
}

func TestBackendCommandJSON(t *testing.T) {
	var stdout bytes.Buffer
	app := App{
		Detector: fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		Env:      func(string) string { return "" },
		Stdout:   &stdout,
	}

	code := app.Run(context.Background(), []string{"--json", "backend"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if got := strings.TrimSpace(stdout.String()); got != `{"backend":"systemd"}` {
		t.Fatalf("stdout = %q, want JSON backend", got)
	}
}

func TestBackendDetectionFailureExitCode(t *testing.T) {
	var stderr bytes.Buffer
	app := App{
		Detector: fakeBackendDetector{err: errors.New("no supported init backend detected")},
		Env:      func(string) string { return "" },
		Stderr:   &stderr,
	}

	code := app.Run(context.Background(), []string{"backend"})
	if code != exitRuntimeError {
		t.Fatalf("Run() exit = %d, want %d", code, exitRuntimeError)
	}
	if !strings.Contains(stderr.String(), "backend detection failed") {
		t.Fatalf("stderr = %q, want detection failure", stderr.String())
	}
}

func TestActiveServiceCommandText(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  string
		want string
	}{
		{"status", "status", "mysql state=started backend=systemd service=mysql.service"},
		{"is-active", "is-active", "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			app := statusApp(servicemgr.ServiceStatus{
				Service: "mysql", Backend: servicemgr.BackendSystemd,
				Unit: "mysql.service", Status: servicemgr.StatusActive,
			}, nil, &stdout, nil)

			code := app.Run(context.Background(), []string{tc.cmd, "mysql"})
			if code != exitSuccess {
				t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
			}
			if got := strings.TrimSpace(stdout.String()); got != tc.want {
				t.Fatalf("stdout = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStatusUsesDaemonStateWhenAvailable(t *testing.T) {
	var stdout bytes.Buffer
	app := statusApp(servicemgr.ServiceStatus{
		Service: "mysql", Backend: servicemgr.BackendSystemd,
		Unit: "mysql.service", Status: servicemgr.StatusInactive,
	}, nil, &stdout, nil)
	app.FetchDaemonServiceState = func(context.Context, options, string) (string, bool) {
		return "starting", true
	}

	code := app.Run(context.Background(), []string{"status", "mysql"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if got := strings.TrimSpace(stdout.String()); got != "mysql state=starting backend=systemd service=mysql.service" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestStatusUsesRequestedServiceForDaemonState(t *testing.T) {
	var stdout bytes.Buffer
	app := statusApp(servicemgr.ServiceStatus{
		Service: "sshd", Backend: servicemgr.BackendOpenRC,
		Unit: "sshd", Status: servicemgr.StatusActive,
	}, nil, &stdout, nil)
	var requested string
	app.FetchDaemonServiceState = func(_ context.Context, _ options, service string) (string, bool) {
		requested = service
		return "monitored", true
	}

	code := app.Run(context.Background(), []string{"status", "ssh-temp"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if requested != "ssh-temp" {
		t.Fatalf("daemon state requested for %q, want ssh-temp", requested)
	}
	if got := strings.TrimSpace(stdout.String()); got != "sshd state=monitored backend=openrc service=sshd" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestStatusCommandJSON(t *testing.T) {
	var stdout bytes.Buffer
	app := statusApp(servicemgr.ServiceStatus{
		Service: "mysql", Backend: servicemgr.BackendSystemd,
		Unit: "mysql.service", Status: servicemgr.StatusActive,
	}, nil, &stdout, nil)

	code := app.Run(context.Background(), []string{"--json", "status", "mysql"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	want := `{"service":"mysql","state":"started","backend":"systemd","status":"active","unit":"mysql.service","paused":false}`
	if got := strings.TrimSpace(stdout.String()); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestStatusCommandUsesResolvedConfiguredUnit(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "catalog")
	mustWrite(t, filepath.Join(root, "sermo.yml"), `
paths:
  services: [`+filepath.Join(root, "services")+`]
defaults:
  policy: { cooldown: 5m }
`)
	mustWrite(t, filepath.Join(root, "catalog", "services", "rpc-mountd.yml"), `
name: rpc-mountd
service:
  systemd: [nfs-mountd, rpc-mountd]
checks:
  service: { type: service, expect: active }
`)
	mustWrite(t, filepath.Join(root, "services", "rpc-mountd.yml"), `
name: rpc-mountd
uses: rpc-mountd
`)
	cfg, err := config.Load(filepath.Join(root, "sermo.yml"), config.WithCatalogDirs(catalogDir))
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var statusCalls []string
	app := App{
		Detector: fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		NewManager: fakeStatusManager(servicemgr.ServiceStatus{
			Service: "rpc-mountd", Backend: servicemgr.BackendSystemd,
			Unit: "nfs-mountd.service", Status: servicemgr.StatusActive,
		}, &statusCalls),
		LoadConfig: func(string, ...config.Option) (*config.Config, error) { return cfg, nil },
		Runner:     statusUnitRunner{known: "nfs-mountd.service"},
		Env:        func(string) string { return "" },
		Stdout:     &stdout,
		Stderr:     &stderr,
	}

	code := app.Run(context.Background(), []string{"status", "rpc-mountd"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d; stderr=%s", code, exitSuccess, stderr.String())
	}
	if len(statusCalls) == 0 {
		t.Fatal("manager Status was not called")
	}
	for _, call := range statusCalls {
		if call == "rpc-mountd" || call == "rpc-mountd.service" {
			t.Fatalf("Status called with unresolved unit %q; calls=%v", call, statusCalls)
		}
	}
	if got := statusCalls[len(statusCalls)-1]; got != "nfs-mountd.service" {
		t.Fatalf("last Status call = %q, want nfs-mountd.service; calls=%v", got, statusCalls)
	}
}

func writeFallbackUnitConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  services: [`+filepath.Join(root, "services")+`]
  runtime: `+filepath.Join(root, "run")+`
defaults:
  policy: { cooldown: 5m }
`)
	mustWrite(t, filepath.Join(root, "services", "legacy.yml"), `
name: legacy
service:
  systemd: [legacy-daemon]
  openrc: [legacy-daemon]
`)
	return global
}

func TestStatusFallsBackToConfiguredServiceUnit(t *testing.T) {
	global := writeFallbackUnitConfig(t)
	var stdout, stderr bytes.Buffer
	var statusCalls []string
	app := App{
		LoadConfig: config.Load,
		Detector:   fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		NewManager: fakeStatusManager(servicemgr.ServiceStatus{
			Service: "legacy", Backend: servicemgr.BackendSystemd,
			Unit: "legacy-daemon", Status: servicemgr.StatusActive,
		}, &statusCalls),
		Runner: statusUnitRunner{},
		Env:    func(string) string { return "" },
		Stdout: &stdout,
		Stderr: &stderr,
	}

	code := app.Run(context.Background(), []string{"--config", global, "status", "legacy"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d; stderr=%s", code, exitSuccess, stderr.String())
	}
	if len(statusCalls) == 0 || statusCalls[len(statusCalls)-1] != "legacy-daemon" {
		t.Fatalf("Status calls = %v, want fallback unit legacy-daemon", statusCalls)
	}
	if got := stderr.String(); !strings.Contains(got, "using legacy-daemon") {
		t.Fatalf("stderr = %q, want fallback warning", got)
	}
}

func TestStatusRequiresService(t *testing.T) {
	var stderr bytes.Buffer
	app := statusApp(servicemgr.ServiceStatus{}, nil, nil, &stderr)

	code := app.Run(context.Background(), []string{"status"})
	if code != exitUsage {
		t.Fatalf("Run() exit = %d, want %d", code, exitUsage)
	}
	out := stderr.String()
	if !strings.Contains(out, "status requires a service name") || !strings.Contains(out, "Command: sermoctl status") {
		t.Fatalf("stderr = %q, want focused status usage", out)
	}
}

func TestIsActiveInactiveExitOne(t *testing.T) {
	var stdout bytes.Buffer
	app := statusApp(servicemgr.ServiceStatus{
		Service: "mysql", Backend: servicemgr.BackendSystemd,
		Unit: "mysql.service", Status: servicemgr.StatusInactive,
	}, nil, &stdout, nil)

	code := app.Run(context.Background(), []string{"is-active", "mysql"})
	if code != exitNotActive {
		t.Fatalf("Run() exit = %d, want %d", code, exitNotActive)
	}
}

func TestIsActiveQuietSuppressesOutput(t *testing.T) {
	var stdout bytes.Buffer
	app := statusApp(servicemgr.ServiceStatus{
		Service: "mysql", Backend: servicemgr.BackendSystemd,
		Unit: "mysql.service", Status: servicemgr.StatusInactive,
	}, nil, &stdout, nil)

	code := app.Run(context.Background(), []string{"--quiet", "is-active", "mysql"})
	if code != exitNotActive {
		t.Fatalf("Run() exit = %d, want %d", code, exitNotActive)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestStatusQueryErrorExitTwo(t *testing.T) {
	var stderr bytes.Buffer
	app := statusApp(servicemgr.ServiceStatus{}, errors.New("boom"), nil, &stderr)

	code := app.Run(context.Background(), []string{"status", "mysql"})
	if code != exitRuntimeError {
		t.Fatalf("Run() exit = %d, want %d", code, exitRuntimeError)
	}
	if !strings.Contains(stderr.String(), "status query failed") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func statusApp(status servicemgr.ServiceStatus, statusErr error, stdout, stderr *bytes.Buffer) App {
	if stdout == nil {
		stdout = &bytes.Buffer{}
	}
	if stderr == nil {
		stderr = &bytes.Buffer{}
	}
	return App{
		Detector: fakeBackendDetector{detection: servicemgr.Detection{Backend: status.Backend}},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			return fakeManager{status: status, err: statusErr}, nil
		},
		LoadConfig: func(string, ...config.Option) (*config.Config, error) {
			return nil, errors.New("no test config")
		},
		Env:    func(string) string { return "" },
		Stdout: stdout,
		Stderr: stderr,
	}
}

type statusUnitRunner struct {
	known string
}

func (r statusUnitRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	if name == "systemctl" && len(args) == 3 && args[0] == "cat" && args[1] == "--" && args[2] == r.known {
		return execx.Result{ExitCode: 0}, nil
	}
	return execx.Result{ExitCode: 1}, nil
}

type fakeBackendDetector struct {
	detection servicemgr.Detection
	err       error
}

func (d fakeBackendDetector) Detect(context.Context, servicemgr.Backend) (servicemgr.Detection, error) {
	return d.detection, d.err
}

// fakeStatusManager builds a NewManager factory whose manager reports the given
// status and records every Status call into calls.
func fakeStatusManager(status servicemgr.ServiceStatus, calls *[]string) func(servicemgr.Backend) (servicemgr.Manager, error) {
	return func(servicemgr.Backend) (servicemgr.Manager, error) {
		return fakeManager{status: status, statusCalls: calls}, nil
	}
}

type fakeManager struct {
	status            servicemgr.ServiceStatus
	err               error
	actionErr         error
	actions           *[]string
	statusCalls       *[]string
	supportsReload    *bool
	supportsReloadErr error
}

func (m fakeManager) Status(_ context.Context, service string) (servicemgr.ServiceStatus, error) {
	if m.statusCalls != nil {
		*m.statusCalls = append(*m.statusCalls, service)
	}
	return m.status, m.err
}

func (m fakeManager) Start(_ context.Context, service string) error {
	return m.record("start", service)
}

func (m fakeManager) Stop(_ context.Context, service string) error {
	return m.record("stop", service)
}

func (m fakeManager) Restart(_ context.Context, service string) error {
	return m.record("restart", service)
}

func (m fakeManager) Reload(_ context.Context, service string) error {
	return m.record("reload", service)
}

func (m fakeManager) SupportsReload(_ context.Context, _ string) (bool, error) {
	if m.supportsReload != nil {
		return *m.supportsReload, m.supportsReloadErr
	}
	return true, m.supportsReloadErr
}

func (m fakeManager) ResetState(_ context.Context, service string) error {
	return m.record("reset", service)
}

func (m fakeManager) record(action, service string) error {
	if m.actions != nil {
		*m.actions = append(*m.actions, action+" "+service)
	}
	return m.actionErr
}

func TestReloadRequiresService(t *testing.T) {
	var stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &bytes.Buffer{}, Stderr: &stderr}
	code := app.Run(context.Background(), []string{"reload"})
	if code != exitUsage {
		t.Fatalf("reload without service exit = %d, want %d", code, exitUsage)
	}
	if got := stderr.String(); !strings.Contains(got, "reload requires a service name") || !strings.Contains(got, "daemon reload") {
		t.Fatalf("reload usage error = %q", got)
	}
}

// TestDaemonReloadNoPid exercises the error path for sermoctl daemon reload when no
// sermod pidfile or live process can be found. It proves the command is
// wired and uses the loaded config's runtime dir for pidfile discovery.
func TestDaemonReloadNoPid(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	// Minimal valid global config with a runtime under the temp dir (no pidfile will exist).
	minCfg := []byte("paths:\n  runtime: " + tmp + "\ndefaults:\n  policy:\n    cooldown: 5m\n")
	if err := os.WriteFile(cfgPath, minCfg, 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	app := App{
		LoadConfig: config.Load,
		Stderr:     &stderr,
		Stdout:     &bytes.Buffer{},
		// Hermetic discovery: no absolute fallbacks and a name probe that finds
		// nothing, so reload reliably reports "could not find" rather than picking
		// up a sermod daemon that happens to run on the host.
		FindPID:          func(string) ([]int, error) { return nil, nil },
		pidfileFallbacks: []string{},
	}

	code := app.Run(context.Background(), []string{"--config", cfgPath, "daemon", "reload"})
	if code != exitRuntimeError {
		t.Fatalf("daemon reload exit = %d, want %d", code, exitRuntimeError)
	}
	out := stderr.String()
	if !strings.Contains(out, "could not find") {
		t.Fatalf("stderr did not report pid lookup failure: %q", out)
	}
}

func TestEventsList(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var gotService string
	var gotLimit int
	sample := []event{
		{Time: "2026-06-13T10:05:00Z", Service: "web", Kind: "action", Action: "restart", Status: "ok", Message: "restarted"},
		{Time: "2026-06-13T10:00:00Z", Watch: "storage-root", Kind: "alert", Message: "high usage"},
		{Time: "2026-06-13T09:58:00Z", App: "salt-minion", Kind: "firing", Message: "error: cancelled"},
		{Time: "2026-06-13T09:55:00Z", Service: "web", Kind: "recovered", Rule: "alert-if-memory-high", Message: "rule condition recovered"},
	}
	app := App{
		FetchEvents: func(ctx context.Context, opts options, service string, limit int) ([]event, error) {
			gotService = service
			gotLimit = limit
			return sample, nil
		},
		Stdout: &stdout,
		Stderr: &stderr,
		Stdin:  strings.NewReader(""),
	}

	// global list
	code := app.runEvents(context.Background(), options{args: nil, json: false})
	if code != exitSuccess {
		t.Fatalf("events list exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "web") || !strings.Contains(out, "storage-root") || !strings.Contains(out, "restart") {
		t.Fatalf("events list output missing data:\n%s", out)
	}
	// An app event's target is the app name, never the "-" placeholder.
	if !strings.Contains(out, "salt-minion") {
		t.Fatalf("events list must show the app as target:\n%s", out)
	}
	// The rule column disambiguates several rules of one service recovering in
	// the same cycle (they used to render as identical rows).
	if !strings.Contains(out, "RULE") || !strings.Contains(out, "alert-if-memor") {
		t.Fatalf("events list must show the rule column:\n%s", out)
	}
	if gotService != "" || gotLimit != defaultEventsListLimit {
		t.Fatalf("events list query = (%q, %d), want (%q, %d)", gotService, gotLimit, "", defaultEventsListLimit)
	}

	// json
	stdout.Reset()
	code = app.runEvents(context.Background(), options{args: []string{"web"}, eventLimit: 7, json: true})
	if code != exitSuccess {
		t.Fatalf("events json exit=%d", code)
	}
	if !strings.Contains(stdout.String(), `"service":"web"`) {
		t.Fatalf("json events missing service: %s", stdout.String())
	}
	if gotService != "web" || gotLimit != 7 {
		t.Fatalf("events json query = (%q, %d), want (%q, %d)", gotService, gotLimit, "web", 7)
	}
}

func TestEventActivityClear(t *testing.T) {
	cutoff := time.Date(2026, 6, 13, 10, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		args    []string
		before  time.Time // expected cutoff passed to PruneEvents (zero => none)
		ret     int
		wantOut string
	}{
		{"events", []string{"events", "clear"}, time.Time{}, 3, "cleared 3 events\n"},
		{"activity", []string{"activity", "clear"}, time.Time{}, 4, "cleared 4 activity entries\n"},
		{"activity before", []string{"activity", "clear", "--before", cutoff.Format(time.RFC3339)}, cutoff, 2, "cleared 2 activity entries before 2026-06-13T10:30:00Z\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			called := false
			app := App{
				Env:        func(string) string { return "" },
				LoadConfig: func(string, ...config.Option) (*config.Config, error) { return &config.Config{}, nil },
				PruneEvents: func(_ context.Context, _ options, before time.Time) (int, error) {
					called = true
					if !before.Equal(tc.before) {
						t.Fatalf("before = %v, want %v", before, tc.before)
					}
					return tc.ret, nil
				},
				Stdout: &stdout,
				Stderr: &stderr,
				Stdin:  strings.NewReader(""),
			}
			code := app.Run(context.Background(), tc.args)
			if code != exitSuccess {
				t.Fatalf("%v exit=%d stderr=%s", tc.args, code, stderr.String())
			}
			if !called {
				t.Fatal("clear did not prune events")
			}
			if got := stdout.String(); got != tc.wantOut {
				t.Fatalf("output = %q, want %q", got, tc.wantOut)
			}
		})
	}
}

func TestParseBeforeRejectsUnsafeCutoffs(t *testing.T) {
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	tests := []string{
		"0",
		"-1h",
		now.Add(time.Hour).Format(time.RFC3339),
	}
	for _, input := range tests {
		if got, err := parseBefore(input, func() time.Time { return now }); err == nil {
			t.Fatalf("parseBefore(%q) = %v, want error", input, got)
		}
	}
}

func TestActivityRequiresClear(t *testing.T) {
	var stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &bytes.Buffer{}, Stderr: &stderr}
	code := app.Run(context.Background(), []string{"activity"})
	if code != exitUsage {
		t.Fatalf("activity without clear exit=%d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "activity supports only") {
		t.Fatalf("activity usage error missing detail: %q", stderr.String())
	}
}

// TestDaemonReloadPidProbeFallback exercises the by-name discovery fallback inside
// runReload using an injected FindPID. It verifies that when no pidfile exists,
// the code resolves the daemon pid natively (no pidof/pgrep shell-out) and
// attempts to signal it (the synthetic pid causes a signal error, which is the
// expected outcome in the test environment).
func TestDaemonReloadPidProbeFallback(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	minCfg := []byte("paths:\n  runtime: " + tmp + "\ndefaults:\n  policy:\n    cooldown: 5m\n")
	if err := os.WriteFile(cfgPath, minCfg, 0o644); err != nil {
		t.Fatal(err)
	}

	// Native by-name probe returns a synthetic pid; the Kill will then fail.
	var probedName string
	findPID := func(name string) ([]int, error) {
		probedName = name
		return []int{42424}, nil
	}

	var stderr bytes.Buffer
	app := App{
		LoadConfig: config.Load,
		Stderr:     &stderr,
		Stdout:     &bytes.Buffer{},
		FindPID:    findPID,
		// Suppress the absolute /run fallbacks so discovery is hermetic: only the
		// (empty) temp runtime dir is searched, forcing the probe path under test.
		pidfileFallbacks: []string{},
	}

	code := app.Run(context.Background(), []string{"--config", cfgPath, "daemon", "reload"})
	if code != exitRuntimeError {
		t.Fatalf("daemon reload exit = %d, want %d (signal on fake pid from probe should fail)", code, exitRuntimeError)
	}

	// The native probe must be consulted for the daemon's program name.
	if probedName != "sermod" {
		t.Fatalf("FindPID probed %q, want %q", probedName, "sermod")
	}

	out := stderr.String()
	if !strings.Contains(out, "42424") || !strings.Contains(out, "failed to signal") {
		t.Fatalf("stderr did not show probe-derived pid + signal failure: %q", out)
	}
}
