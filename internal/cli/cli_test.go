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

type fakeBackendDetector struct {
	detection servicemgr.Detection
	err       error
}

func (d fakeBackendDetector) Detect(context.Context, servicemgr.Backend) (servicemgr.Detection, error) {
	return d.detection, d.err
}
