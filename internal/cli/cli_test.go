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

func TestHelpCommandPrintsStructuredUsage(t *testing.T) {
	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}

	code := app.Run(context.Background(), []string{"--help"})
	if code != exitSuccess {
		t.Fatalf("--help exit = %d, want %d", code, exitSuccess)
	}
	out := stdout.String()
	for _, want := range []string{
		"Sermo operator CLI",
		"Usage:",
		"Global Flags:",
		"Safe Service Operations:",
		"sermoctl help [COMMAND]",
		"Use `sermoctl help COMMAND`",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("--help output missing %q:\n%s", want, out)
		}
	}
}

func TestHelpCommandTopic(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &stderr}

	code := app.Run(context.Background(), []string{"help", "restart"})
	if code != exitSuccess {
		t.Fatalf("help restart exit = %d, want %d; stderr=%s", code, exitSuccess, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"Command: sermoctl restart",
		"sermoctl restart SERVICE [--no-cascade]",
		"--no-cascade",
		"Manual restarts are not remediation-rate-limited",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("help restart output missing %q:\n%s", want, out)
		}
	}
}

func TestHelpVersionTopicDoesNotPrintVersion(t *testing.T) {
	var stdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}

	code := app.Run(context.Background(), []string{"help", "version"})
	if code != exitSuccess {
		t.Fatalf("help version exit = %d, want %d", code, exitSuccess)
	}
	out := stdout.String()
	if !strings.Contains(out, "Command: sermoctl version") || strings.HasPrefix(out, "sermo ") {
		t.Fatalf("help version output = %q", out)
	}
}

func TestCommandHelpFlagShowsFocusedHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &stderr}

	code := app.Run(context.Background(), []string{"status", "--help"})
	if code != exitSuccess {
		t.Fatalf("status --help exit = %d, want %d; stderr=%s", code, exitSuccess, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Command: sermoctl status") || !strings.Contains(out, "sermoctl status SERVICE") {
		t.Fatalf("status --help output = %q", out)
	}
}

func TestUnknownHelpTopicIsUsageError(t *testing.T) {
	var stderr bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &bytes.Buffer{}, Stderr: &stderr}

	code := app.Run(context.Background(), []string{"help", "not-a-command"})
	if code != exitUsage {
		t.Fatalf("help not-a-command exit = %d, want %d", code, exitUsage)
	}
	out := stderr.String()
	if !strings.Contains(out, `unknown help topic "not-a-command"`) || !strings.Contains(out, "Command: sermoctl help") {
		t.Fatalf("unknown help topic stderr = %q", out)
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

func TestStatusCommandText(t *testing.T) {
	var stdout bytes.Buffer
	app := statusApp(servicemgr.ServiceStatus{
		Service: "mysql", Backend: servicemgr.BackendSystemd,
		Unit: "mysql.service", Status: servicemgr.StatusActive,
	}, nil, &stdout, nil)

	code := app.Run(context.Background(), []string{"status", "mysql"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if got := strings.TrimSpace(stdout.String()); got != "mysql state=running backend=systemd service=mysql.service" {
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
	want := `{"service":"mysql","state":"running","backend":"systemd","status":"active","unit":"mysql.service","paused":false}`
	if got := strings.TrimSpace(stdout.String()); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestStatusCommandUsesResolvedConfiguredUnit(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "sermo.yml"), `
paths:
  catalog: [`+filepath.Join(root, "catalog")+`]
  services: [`+filepath.Join(root, "services")+`]
defaults:
  policy: { cooldown: 5m }
`)
	mustWrite(t, filepath.Join(root, "catalog", "services", "rpc-mountd.yml"), `
kind: daemon
name: rpc-mountd
service:
  systemd: [nfs-mountd, rpc-mountd]
checks:
  service: { type: service, expect: active }
`)
	mustWrite(t, filepath.Join(root, "services", "rpc-mountd.yml"), `
kind: service
name: rpc-mountd
uses: rpc-mountd
`)
	cfg, err := config.Load(filepath.Join(root, "sermo.yml"))
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var statusCalls []string
	app := App{
		Detector: fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			return fakeManager{
				status: servicemgr.ServiceStatus{
					Service: "rpc-mountd", Backend: servicemgr.BackendSystemd,
					Unit: "nfs-mountd.service", Status: servicemgr.StatusActive,
				},
				statusCalls: &statusCalls,
			}, nil
		},
		LoadConfig: func(string, ...config.Option) (*config.Config, error) { return cfg, nil },
		Runner:     statusUnitRunner{known: "nfs-mountd.service"},
		Env:        func(string) string { return "" },
		Stdout:     &stdout,
		Stderr:     &bytes.Buffer{},
	}

	code := app.Run(context.Background(), []string{"status", "rpc-mountd"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
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

func TestIsActiveActiveExitZero(t *testing.T) {
	var stdout bytes.Buffer
	app := statusApp(servicemgr.ServiceStatus{
		Service: "mysql", Backend: servicemgr.BackendSystemd,
		Unit: "mysql.service", Status: servicemgr.StatusActive,
	}, nil, &stdout, nil)

	code := app.Run(context.Background(), []string{"is-active", "mysql"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if got := strings.TrimSpace(stdout.String()); got != "active" {
		t.Fatalf("stdout = %q, want active", got)
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

type fakeManager struct {
	status      servicemgr.ServiceStatus
	err         error
	actionErr   error
	actions     *[]string
	statusCalls *[]string
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
	return true, nil
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
	sample := []event{
		{Time: "2026-06-13T10:05:00Z", Service: "web", Kind: "action", Action: "restart", Status: "ok", Message: "restarted"},
		{Time: "2026-06-13T10:00:00Z", Watch: "storage-root", Kind: "alert", Message: "high usage"},
	}
	app := App{
		FetchEvents: func(ctx context.Context, opts options, service string, limit int) ([]event, error) {
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

	// json
	stdout.Reset()
	code = app.runEvents(context.Background(), options{args: []string{"web"}, json: true})
	if code != exitSuccess {
		t.Fatalf("events json exit=%d", code)
	}
	if !strings.Contains(stdout.String(), `"service":"web"`) {
		t.Fatalf("json events missing service: %s", stdout.String())
	}
}

func TestEventsClear(t *testing.T) {
	var stdout bytes.Buffer
	app := App{
		Env: func(string) string { return "" },
		PruneEvents: func(ctx context.Context, opts options, before time.Time) (int, error) {
			if !before.IsZero() {
				t.Fatalf("before = %v, want zero time", before)
			}
			return 3, nil
		},
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		Stdin:  strings.NewReader(""),
	}
	code := app.Run(context.Background(), []string{"events", "clear"})
	if code != exitSuccess {
		t.Fatalf("events clear exit=%d", code)
	}
	if got := stdout.String(); got != "cleared 3 events\n" {
		t.Fatalf("events clear output = %q", got)
	}
}

func TestActivityClear(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var called bool
	app := App{
		Env: func(string) string { return "" },
		PruneEvents: func(ctx context.Context, opts options, before time.Time) (int, error) {
			called = true
			if !before.IsZero() {
				t.Fatalf("before = %v, want zero time", before)
			}
			return 4, nil
		},
		Stdout: &stdout,
		Stderr: &stderr,
		Stdin:  strings.NewReader(""),
	}
	code := app.Run(context.Background(), []string{"activity", "clear"})
	if code != exitSuccess {
		t.Fatalf("activity clear exit=%d stderr=%s", code, stderr.String())
	}
	if !called {
		t.Fatal("activity clear did not prune events")
	}
	if got := stdout.String(); got != "cleared 4 activity entries\n" {
		t.Fatalf("activity clear output = %q", got)
	}
}

func TestActivityClearBefore(t *testing.T) {
	var stdout bytes.Buffer
	want := time.Date(2026, 6, 13, 10, 30, 0, 0, time.UTC)
	app := App{
		Env: func(string) string { return "" },
		PruneEvents: func(ctx context.Context, opts options, before time.Time) (int, error) {
			if !before.Equal(want) {
				t.Fatalf("before = %s, want %s", before.Format(time.RFC3339), want.Format(time.RFC3339))
			}
			return 2, nil
		},
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		Stdin:  strings.NewReader(""),
	}
	code := app.Run(context.Background(), []string{"activity", "clear", "--before", want.Format(time.RFC3339)})
	if code != exitSuccess {
		t.Fatalf("activity clear --before exit=%d", code)
	}
	if got := stdout.String(); got != "cleared 2 activity entries before 2026-06-13T10:30:00Z\n" {
		t.Fatalf("activity clear --before output = %q", got)
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
