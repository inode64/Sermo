package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/config"
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

func (m fakeManager) ResetState(_ context.Context, service string) error {
	return m.record("reset", service)
}

func (m fakeManager) record(action, service string) error {
	if m.actions != nil {
		*m.actions = append(*m.actions, action+" "+service)
	}
	return m.actionErr
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
		// Hermetic discovery: no absolute fallbacks and a name probe that finds
		// nothing, so reload reliably reports "could not find" rather than picking
		// up a sermod daemon that happens to run on the host.
		FindPID:          func(string) ([]int, error) { return nil, nil },
		pidfileFallbacks: []string{},
	}

	code := app.Run(context.Background(), []string{"--config", cfgPath, "reload"})
	if code != exitRuntimeError {
		t.Fatalf("reload exit = %d, want %d", code, exitRuntimeError)
	}
	out := stderr.String()
	if !strings.Contains(out, "could not find") {
		t.Fatalf("stderr did not report pid lookup failure: %q", out)
	}
}

// TestReloadPidProbeFallback exercises the by-name discovery fallback inside
// runReload using an injected FindPID. It verifies that when no pidfile exists,
// the code resolves the daemon pid natively (no pidof/pgrep shell-out) and
// attempts to signal it (the synthetic pid causes a signal error, which is the
// expected outcome in the test environment).
func TestReloadPidProbeFallback(t *testing.T) {
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

	code := app.Run(context.Background(), []string{"--config", cfgPath, "reload"})
	if code != exitRuntimeError {
		t.Fatalf("reload exit = %d, want %d (signal on fake pid from probe should fail)", code, exitRuntimeError)
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
