package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/operation"
	"sermo/internal/servicemgr"
)

func writeActionConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  includes: [ `+root+`/enabled ]
  runtime: `+root+`/run
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

type slowDetector struct {
	delay     time.Duration
	detection servicemgr.Detection
}

func (d slowDetector) Detect(ctx context.Context, _ servicemgr.Backend) (servicemgr.Detection, error) {
	timer := time.NewTimer(d.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return d.detection, nil
	case <-ctx.Done():
		return servicemgr.Detection{}, ctx.Err()
	}
}

type deadlineManager struct {
	fakeManager
	remaining *time.Duration
}

func (m deadlineManager) Start(ctx context.Context, service string) error {
	if d, ok := ctx.Deadline(); ok {
		*m.remaining = time.Until(d)
	}
	return m.fakeManager.Start(ctx, service)
}

func TestActionTimeoutNotConsumedByDetection(t *testing.T) {
	global := writeActionConfig(t)
	cfg, err := config.Load(global)
	if err != nil {
		t.Fatal(err)
	}
	resolved, errs := cfg.Resolve("web")
	if len(errs) > 0 {
		t.Fatalf("resolve: %v", errs)
	}

	detectDelay := 80 * time.Millisecond
	opTimeout := 200 * time.Millisecond
	var remaining time.Duration

	app := App{
		LoadConfig: config.Load,
		Detector: slowDetector{
			delay:     detectDelay,
			detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd},
		},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			return deadlineManager{fakeManager: fakeManager{}, remaining: &remaining}, nil
		},
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}

	_, err = app.defaultOperate(context.Background(), options{timeout: opTimeout, config: global}, cfg, resolved, "web", "start")
	if err != nil {
		t.Fatalf("defaultOperate: %v", err)
	}

	// The engine must receive the full operation budget, not detectDelay less.
	if remaining < 150*time.Millisecond {
		t.Fatalf("engine context had %v remaining, want ~%v after %v detect delay", remaining, opTimeout, detectDelay)
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
