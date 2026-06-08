package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/servicemgr"
)

func TestVersionCommand(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-V"} {
		var stdout bytes.Buffer
		app := App{Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &bytes.Buffer{}}
		if code := app.Run(context.Background(), []string{arg}); code != exitSuccess {
			t.Fatalf("Run(%q) exit = %d, want %d", arg, code, exitSuccess)
		}
		if !strings.HasPrefix(stdout.String(), "sermo ") {
			t.Errorf("Run(%q) stdout = %q, want it to start with %q", arg, stdout.String(), "sermo ")
		}
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

func TestInitAliasPrintsDetectedBackend(t *testing.T) {
	var stdout bytes.Buffer
	app := App{
		Detector: fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendOpenRC}},
		Env:      func(string) string { return "" },
		Stdout:   &stdout,
	}

	code := app.Run(context.Background(), []string{"init"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if got := strings.TrimSpace(stdout.String()); got != "openrc" {
		t.Fatalf("stdout = %q, want openrc", got)
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
	if got := strings.TrimSpace(stdout.String()); got != "mysql active backend=systemd service=mysql.service" {
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
	want := `{"service":"mysql","backend":"systemd","status":"active","unit":"mysql.service","paused":false}`
	if got := strings.TrimSpace(stdout.String()); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestStatusRequiresService(t *testing.T) {
	var stderr bytes.Buffer
	app := statusApp(servicemgr.ServiceStatus{}, nil, nil, &stderr)

	code := app.Run(context.Background(), []string{"status"})
	if code != exitUsage {
		t.Fatalf("Run() exit = %d, want %d", code, exitUsage)
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
		Env:    func(string) string { return "" },
		Stdout: stdout,
		Stderr: stderr,
	}
}

type fakeBackendDetector struct {
	detection servicemgr.Detection
	err       error
}

func (d fakeBackendDetector) Detect(context.Context, servicemgr.Backend) (servicemgr.Detection, error) {
	return d.detection, d.err
}

type fakeManager struct {
	status    servicemgr.ServiceStatus
	err       error
	actionErr error
	actions   *[]string
}

func (m fakeManager) Status(context.Context, string) (servicemgr.ServiceStatus, error) {
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

func (m fakeManager) record(action, service string) error {
	if m.actions != nil {
		*m.actions = append(*m.actions, action+" "+service)
	}
	return m.actionErr
}

// recordingProbeRunner is a test double for execx.Runner used to exercise
// the pidof/pgrep fallback in runReload without shelling out. It records
// calls and can be configured to return synthetic stdout for specific probes.
type recordingProbeRunner struct {
	calls []string
	outs  map[string]string // key "cmd arg..." -> stdout to return with exit 0
}

func (r *recordingProbeRunner) Run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	key := name
	if len(args) > 0 {
		key += " " + strings.Join(args, " ")
	}
	r.calls = append(r.calls, key)
	if out, ok := r.outs[key]; ok {
		return execx.Result{Stdout: out, ExitCode: 0}, nil
	}
	// Simulate "not found" / no matching pid (non-zero exit).
	return execx.Result{ExitCode: 1}, fmt.Errorf("exit 1")
}

// TestReloadNoPid exercises the error path for sermoctl reload when no
// sermod pidfile or live process can be found. It proves the command is
// wired and uses the loaded config's runtime dir for pidfile discovery.
func TestReloadNoPid(t *testing.T) {
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
	}

	code := app.Run(context.Background(), []string{"--config", cfgPath, "reload"})
	if code != exitRuntimeError {
		t.Fatalf("reload exit = %d, want %d", code, exitRuntimeError)
	}
	out := stderr.String()
	if !strings.Contains(out, "pid") || (!strings.Contains(out, "could not find") && !strings.Contains(out, "failed to signal")) {
		t.Fatalf("stderr did not report pid lookup/signal failure: %q", out)
	}
}

// TestReloadPidProbeFallback exercises the pidof/pgrep fallback path inside
// runReload using an injected execx.Runner. It verifies that when no pidfile
// exists, the code uses the Runner (with timeout) to probe, parses the pid
// from stdout, and attempts to signal it (the synthetic pid causes a signal
// error, which is the expected outcome in the test environment).
func TestReloadPidProbeFallback(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	minCfg := []byte("paths:\n  runtime: " + tmp + "\ndefaults:\n  policy:\n    cooldown: 5m\n")
	if err := os.WriteFile(cfgPath, minCfg, 0o644); err != nil {
		t.Fatal(err)
	}

	// Configure the runner to make "pidof" succeed with a synthetic pid.
	// pgrep will not be reached (first probe wins). The Kill will then fail.
	runner := &recordingProbeRunner{
		outs: map[string]string{
			"pidof -s sermod": "42424\n",
		},
	}

	var stderr bytes.Buffer
	app := App{
		LoadConfig: config.Load,
		Stderr:     &stderr,
		Stdout:     &bytes.Buffer{},
		Runner:     runner,
	}

	code := app.Run(context.Background(), []string{"--config", cfgPath, "reload"})
	if code != exitRuntimeError {
		t.Fatalf("reload exit = %d, want %d (signal on fake pid from probe should fail)", code, exitRuntimeError)
	}

	// Assert the probe was actually invoked through the Runner (this is the
	// key coverage for switching away from raw exec.Command).
	if len(runner.calls) == 0 || runner.calls[0] != "pidof -s sermod" {
		t.Fatalf("runner calls = %v, want first call to be pidof probe via execx.Runner", runner.calls)
	}

	out := stderr.String()
	if !strings.Contains(out, "42424") || !strings.Contains(out, "failed to signal") {
		t.Fatalf("stderr did not show probe-derived pid + signal failure: %q", out)
	}
}
