package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/config"
	"sermo/internal/operation"
)

func writeActionConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  enabled: [ `+root+`/enabled ]
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "enabled", "web.yml"), `
kind: service
name: web
service: { name: web }
`)
	return global
}

// actionApp builds an App whose operation engine is replaced by a canned result.
func actionApp(result operation.Result, opErr error, stdout, stderr *bytes.Buffer) App {
	if stdout == nil {
		stdout = &bytes.Buffer{}
	}
	if stderr == nil {
		stderr = &bytes.Buffer{}
	}
	return App{
		LoadConfig: config.Load,
		Operate: func(context.Context, options, *config.Config, config.Resolved, string, string) (operation.Result, error) {
			return result, opErr
		},
		Env:    func(string) string { return "" },
		Stdout: stdout,
		Stderr: stderr,
	}
}

func TestRestartOKThroughEngine(t *testing.T) {
	global := writeActionConfig(t)
	var stdout bytes.Buffer
	app := actionApp(operation.Result{Service: "web", Action: "restart", Status: operation.ResultOK}, nil, &stdout, nil)

	code := app.Run(context.Background(), []string{"--config", global, "restart", "web"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if got := strings.TrimSpace(stdout.String()); got != "web restart ok" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestRestartBlockedExit75(t *testing.T) {
	global := writeActionConfig(t)
	var stdout bytes.Buffer
	app := actionApp(operation.Result{Service: "web", Action: "restart", Status: operation.ResultBlocked, Message: "backup running"}, nil, &stdout, nil)

	code := app.Run(context.Background(), []string{"--config", global, "restart", "web"})
	if code != exitBlocked {
		t.Fatalf("Run() exit = %d, want %d (blocked)", code, exitBlocked)
	}
	out := stdout.String()
	if !strings.Contains(out, "BLOCKED web restart") || !strings.Contains(out, "reason: backup running") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestPreflightFailedExit1(t *testing.T) {
	global := writeActionConfig(t)
	var stdout bytes.Buffer
	app := actionApp(operation.Result{Service: "web", Action: "restart", Status: operation.ResultPreflightFailed, Message: "preflight failed"}, nil, &stdout, nil)

	code := app.Run(context.Background(), []string{"--config", global, "restart", "web"})
	if code != exitNotActive {
		t.Fatalf("Run() exit = %d, want %d", code, exitNotActive)
	}
}

func TestActionFailedExit2(t *testing.T) {
	global := writeActionConfig(t)
	app := actionApp(operation.Result{Service: "web", Action: "stop", Status: operation.ResultFailed, Message: "stop: boom"}, nil, nil, nil)

	code := app.Run(context.Background(), []string{"--config", global, "stop", "web"})
	if code != exitRuntimeError {
		t.Fatalf("Run() exit = %d, want %d", code, exitRuntimeError)
	}
}

func TestActionJSON(t *testing.T) {
	global := writeActionConfig(t)
	var stdout bytes.Buffer
	app := actionApp(operation.Result{Service: "web", Action: "restart", Status: operation.ResultOK, Backend: "systemd"}, nil, &stdout, nil)

	code := app.Run(context.Background(), []string{"--config", global, "--json", "restart", "web"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	var got struct {
		Service string `json:"service"`
		Action  string `json:"action"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json: %v (out=%s)", err, stdout.String())
	}
	if got.Service != "web" || got.Action != "restart" || got.Status != "ok" {
		t.Fatalf("unexpected JSON: %+v", got)
	}
}

func TestActionWiringErrorExit2(t *testing.T) {
	global := writeActionConfig(t)
	var stderr bytes.Buffer
	app := actionApp(operation.Result{}, errors.New("backend detection failed: none"), nil, &stderr)

	code := app.Run(context.Background(), []string{"--config", global, "restart", "web"})
	if code != exitRuntimeError {
		t.Fatalf("Run() exit = %d, want %d", code, exitRuntimeError)
	}
	if !strings.Contains(stderr.String(), "backend detection failed") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestActionUnknownService(t *testing.T) {
	global := writeActionConfig(t)
	var stderr bytes.Buffer
	app := actionApp(operation.Result{}, nil, nil, &stderr)

	code := app.Run(context.Background(), []string{"--config", global, "restart", "nope"})
	if code != exitRuntimeError {
		t.Fatalf("Run() exit = %d, want %d", code, exitRuntimeError)
	}
	if !strings.Contains(stderr.String(), "unknown service") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestActionRequiresService(t *testing.T) {
	var stderr bytes.Buffer
	app := actionApp(operation.Result{}, nil, nil, &stderr)

	code := app.Run(context.Background(), []string{"stop"})
	if code != exitUsage {
		t.Fatalf("Run() exit = %d, want %d", code, exitUsage)
	}
}
