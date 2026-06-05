package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"sermo/internal/servicemgr"
)

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
	want := `{"service":"mysql","backend":"systemd","status":"active","unit":"mysql.service"}`
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
