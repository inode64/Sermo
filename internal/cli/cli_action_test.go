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
	"sermo/internal/execx"
	"sermo/internal/operation"
	"sermo/internal/servicemgr"
)

func writeActionConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  services: [ `+root+`/enabled ]
  runtime: `+root+`/run
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "enabled", "web.yml"), `
kind: service
name: web
service: web
`)
	return global
}

func writeInvalidActionConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  services: [ `+root+`/enabled ]
  runtime: `+root+`/run
  locks: `+root+`/locks
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "enabled", "web.yml"), `
kind: service
name: web
service: web
`)
	return global
}

func writeReloadCommandConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  services: [ `+root+`/enabled ]
  runtime: `+root+`/run
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "enabled", "web.yml"), `
kind: service
name: web
service: web
reload:
  command: [reload-web, --check]
  when: always
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

func TestRestartUsesCanonicalServiceAlias(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  services: [ `+root+`/enabled ]
  runtime: `+root+`/run
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "enabled", "web.yml"), `
kind: service
name: web
aliases: [frontend]
service: web
`)

	var stdout bytes.Buffer
	var gotService string
	app := actionApp(operation.Result{}, nil, &stdout, nil)
	app.Operate = func(_ context.Context, _ options, _ *config.Config, _ config.Resolved, service, action string) (operation.Result, error) {
		gotService = service
		return operation.Result{Service: service, Action: action, Status: operation.ResultOK}, nil
	}

	code := app.Run(context.Background(), []string{"--config", global, "restart", "frontend"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if gotService != "web" {
		t.Fatalf("Operate service = %q, want web", gotService)
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

func TestRestartBlockedByBackupNotifiesInteractiveUser(t *testing.T) {
	global := writeActionConfig(t)
	app := actionApp(operation.Result{Service: "db", Action: "restart", Status: operation.ResultBlocked, Message: "database backup is running"}, nil, nil, nil)
	app.InteractiveUser = func() (string, bool) { return "fran", true }
	var notified operation.Result
	var notifiedUser string
	app.NotifyBlockedAction = func(_ context.Context, result operation.Result, user string) error {
		notified = result
		notifiedUser = user
		return nil
	}

	code := app.Run(context.Background(), []string{"--config", global, "restart", "web"})
	if code != exitBlocked {
		t.Fatalf("Run() exit = %d, want %d", code, exitBlocked)
	}
	if notified.Service != "db" || notified.Action != "restart" || notifiedUser != "fran" {
		t.Fatalf("notified result=%+v user=%q", notified, notifiedUser)
	}
}

func TestRestartBlockedByBackupDoesNotNotifyCron(t *testing.T) {
	global := writeActionConfig(t)
	app := actionApp(operation.Result{Service: "db", Action: "restart", Status: operation.ResultBlocked, Message: "database backup is running"}, nil, nil, nil)
	app.InteractiveUser = func() (string, bool) { return "", false }
	app.NotifyBlockedAction = func(context.Context, operation.Result, string) error {
		t.Fatal("cron/non-interactive action must not notify a terminal user")
		return nil
	}

	code := app.Run(context.Background(), []string{"--config", global, "restart", "web"})
	if code != exitBlocked {
		t.Fatalf("Run() exit = %d, want %d", code, exitBlocked)
	}
}

func TestRestartBlockedForNonBackupDoesNotNotify(t *testing.T) {
	global := writeActionConfig(t)
	app := actionApp(operation.Result{Service: "db", Action: "restart", Status: operation.ResultBlocked, Message: "configuration invalid"}, nil, nil, nil)
	app.InteractiveUser = func() (string, bool) { return "fran", true }
	app.NotifyBlockedAction = func(context.Context, operation.Result, string) error {
		t.Fatal("non-backup block must not notify through tty")
		return nil
	}

	code := app.Run(context.Background(), []string{"--config", global, "restart", "web"})
	if code != exitBlocked {
		t.Fatalf("Run() exit = %d, want %d", code, exitBlocked)
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

// writeCascadeConfig sets up a primary `web` that cascades to `db` via
// also_apply, so restart web runs both services through Operate.
func writeCascadeConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  services: [ `+root+`/enabled ]
  runtime: `+root+`/run
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "enabled", "web.yml"), `
kind: service
name: web
service: web
also_apply: [db]
`)
	mustWrite(t, filepath.Join(root, "enabled", "db.yml"), `
kind: service
name: db
service: db
`)
	return global
}

// TestCascadeTargetErrorDowngradesPrimary verifies that a cascade target whose
// Operate returns an error (not just a failed result) downgrades the primary so
// the exit code reflects the failure.
func TestCascadeTargetErrorDowngradesPrimary(t *testing.T) {
	global := writeCascadeConfig(t)
	app := actionApp(operation.Result{}, nil, nil, nil)
	app.Operate = func(_ context.Context, _ options, _ *config.Config, _ config.Resolved, service, action string) (operation.Result, error) {
		if service == "db" {
			return operation.Result{}, errors.New("db: engine boom")
		}
		return operation.Result{Service: service, Action: action, Status: operation.ResultOK}, nil
	}

	code := app.Run(context.Background(), []string{"--config", global, "restart", "web"})
	if code != exitRuntimeError {
		t.Fatalf("Run() exit = %d, want %d (cascade target error must downgrade primary)", code, exitRuntimeError)
	}
}

func TestCascadeBackupBlockNotifiesInteractiveUser(t *testing.T) {
	global := writeCascadeConfig(t)
	app := actionApp(operation.Result{}, nil, nil, nil)
	app.InteractiveUser = func() (string, bool) { return "fran", true }
	var notified operation.Result
	app.NotifyBlockedAction = func(_ context.Context, result operation.Result, _ string) error {
		notified = result
		return nil
	}
	app.Operate = func(_ context.Context, _ options, _ *config.Config, _ config.Resolved, service, action string) (operation.Result, error) {
		if service == "db" {
			return operation.Result{Service: service, Action: action, Status: operation.ResultBlocked, Message: "database backup is running"}, nil
		}
		return operation.Result{Service: service, Action: action, Status: operation.ResultOK}, nil
	}

	code := app.Run(context.Background(), []string{"--config", global, "restart", "web"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if notified.Service != "db" || notified.Action != "restart" {
		t.Fatalf("notified result = %+v, want cascade db restart", notified)
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

func TestReloadValidatesConfigBeforeOperate(t *testing.T) {
	global := writeInvalidActionConfig(t)
	var stderr bytes.Buffer
	called := false
	app := actionApp(operation.Result{Service: "web", Action: "reload", Status: operation.ResultOK}, nil, nil, &stderr)
	app.Operate = func(context.Context, options, *config.Config, config.Resolved, string, string) (operation.Result, error) {
		called = true
		return operation.Result{}, nil
	}

	code := app.Run(context.Background(), []string{"--config", global, "reload", "web"})
	if code != exitConfigInvalid {
		t.Fatalf("Run() exit = %d, want %d", code, exitConfigInvalid)
	}
	if called {
		t.Fatal("reload operated despite invalid configuration")
	}
	if !strings.Contains(stderr.String(), "ERROR global") {
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

type reloadRecordingRunner struct {
	calls [][]string
}

func (r *reloadRecordingRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return execx.Result{}, nil
}

func (r *reloadRecordingRunner) ran(name string) bool {
	for _, call := range r.calls {
		if len(call) > 0 && call[0] == name {
			return true
		}
	}
	return false
}

func TestReloadNativeCommandUsesAppRunner(t *testing.T) {
	global := writeReloadCommandConfig(t)
	runner := &reloadRecordingRunner{}
	var actions []string
	var stdout bytes.Buffer
	app := App{
		LoadConfig: config.Load,
		Detector:   fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendOpenRC}},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			return fakeManager{actions: &actions}, nil
		},
		Runner: runner,
		Env:    func(string) string { return "" },
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
	}

	code := app.Run(context.Background(), []string{"--config", global, "reload", "web"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d; stdout=%q", code, exitSuccess, stdout.String())
	}
	if !runner.ran("reload-web") {
		t.Fatalf("native reload command did not use App.Runner; calls=%v", runner.calls)
	}
	if len(actions) != 0 {
		t.Fatalf("reload command with when=always should not call backend manager; actions=%v", actions)
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

func TestDefaultOperateFallsBackToConfiguredServiceUnit(t *testing.T) {
	global := writeFallbackUnitConfig(t)
	cfg, err := config.Load(global)
	if err != nil {
		t.Fatal(err)
	}
	resolved, errs := cfg.Resolve("legacy")
	if len(errs) > 0 {
		t.Fatalf("resolve: %v", errs)
	}

	var actions []string
	var stderr bytes.Buffer
	app := App{
		LoadConfig: config.Load,
		Detector:   fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			return fakeManager{actions: &actions}, nil
		},
		Runner: statusUnitRunner{},
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
	}

	result, err := app.defaultOperate(context.Background(), options{config: global}, cfg, resolved, "legacy", "start")
	if err != nil {
		t.Fatalf("defaultOperate: %v", err)
	}
	if result.Status != operation.ResultOK {
		t.Fatalf("result = %+v, want ok", result)
	}
	if len(actions) != 1 || actions[0] != "start legacy-daemon" {
		t.Fatalf("actions = %v, want start legacy-daemon", actions)
	}
	if got := stderr.String(); !strings.Contains(got, "using legacy-daemon") {
		t.Fatalf("stderr = %q, want fallback warning", got)
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
