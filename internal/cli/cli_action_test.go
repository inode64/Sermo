package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlite3 "modernc.org/sqlite/lib"

	"sermo/internal/config"
	"sermo/internal/execx/execxtest"
	"sermo/internal/operation"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
)

func writeActionConfig(t *testing.T) string {
	t.Helper()
	global := writeServiceConfig(t, `
paths:
  services: [ @ROOT@/services ]
  runtime: @ROOT@/run
  state: @ROOT@/state
defaults:
  policy:
    cooldown: 5m
`, map[string]string{
		"services/web.yml": `
name: web
service: web
`,
	})
	return global
}

func writeInvalidActionConfig(t *testing.T) string {
	t.Helper()
	global := writeServiceConfig(t, `
paths:
  services: [ @ROOT@/services ]
  runtime: @ROOT@/run
  locks: @ROOT@/locks
  state: @ROOT@/state
defaults:
  policy:
    cooldown: 5m
`, map[string]string{
		"services/web.yml": `
name: web
service: web
`,
	})
	return global
}

func writeReloadCommandConfig(t *testing.T) string {
	t.Helper()
	global := writeServiceConfig(t, `
paths:
  services: [ @ROOT@/services ]
  runtime: @ROOT@/run
  state: @ROOT@/state
defaults:
  policy:
    cooldown: 5m
`, map[string]string{
		"services/web.yml": `
name: web
service: web
reload:
  command: [reload-web, --check]
  when: always
`,
	})
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

// okOperate is an App.Operate stub that echoes the request as a successful result.
func okOperate(_ context.Context, _ options, _ *config.Config, _ config.Resolved, service, action string) (operation.Result, error) {
	return operation.Result{Service: service, Action: action, Status: operation.ResultOK}, nil
}

// openTestStateStore loads the config at global and opens its state store. The
// caller is responsible for closing the returned store.
func openTestStateStore(t *testing.T, global string) *state.Store {
	t.Helper()
	cfg, err := config.Load(global)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	store, err := state.OpenContextWith(context.Background(), filepath.Join(cfg.Global.StateDir(), state.Filename), state.Options{})
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	return store
}

func readMonitorRecord(t *testing.T, global, service string) state.MonitorRecord {
	t.Helper()
	store := openTestStateStore(t, global)
	defer func() { _ = store.Close() }()
	rec, found, err := store.MonitorState(service)
	if err != nil {
		t.Fatalf("monitor state: %v", err)
	}
	if !found {
		t.Fatalf("monitor state for %s not found", service)
	}
	return rec
}

func readOperationSettling(t *testing.T, global, service string) (state.OperationSettlingRecord, bool) {
	t.Helper()
	store := openTestStateStore(t, global)
	defer func() { _ = store.Close() }()
	rec, found, err := store.OperationSettling(service)
	if err != nil {
		t.Fatalf("operation settling: %v", err)
	}
	return rec, found
}

func writeMonitorRecord(t *testing.T, global, service string, active bool, source string) {
	t.Helper()
	store := openTestStateStore(t, global)
	defer func() { _ = store.Close() }()
	if err := store.SetActive(service, active, source); err != nil {
		t.Fatalf("set active: %v", err)
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
	rec, found := readOperationSettling(t, global, "web")
	if !found || rec.Phase != state.OperationSettlingSettling {
		t.Fatalf("restart settling = %+v found=%v", rec, found)
	}
}

func TestRestartUsesCanonicalServiceAlias(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  services: [ `+root+`/services ]
  runtime: `+root+`/run
  state: `+root+`/state
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "services", "web.yml"), `
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

// assertBlockedDoesNotNotify runs a restart that the engine blocks with reason
// msg under the given interactive context and fails if the CLI notifies a
// terminal user.
func assertBlockedDoesNotNotify(t *testing.T, msg string, interactive func() (string, bool)) {
	t.Helper()
	global := writeActionConfig(t)
	app := actionApp(operation.Result{Service: "db", Action: "restart", Status: operation.ResultBlocked, Message: msg}, nil, nil, nil)
	app.InteractiveUser = interactive
	app.NotifyBlockedAction = func(context.Context, operation.Result, string) error {
		t.Fatal("blocked action must not notify a terminal user")
		return nil
	}

	code := app.Run(context.Background(), []string{"--config", global, "restart", "web"})
	if code != exitBlocked {
		t.Fatalf("Run() exit = %d, want %d", code, exitBlocked)
	}
}

func TestRestartBlockedByBackupDoesNotNotifyCron(t *testing.T) {
	assertBlockedDoesNotNotify(t, "database backup is running", func() (string, bool) { return "", false })
}

func TestRestartBlockedForNonBackupDoesNotNotify(t *testing.T) {
	assertBlockedDoesNotNotify(t, "configuration invalid", func() (string, bool) { return "fran", true })
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

func TestStopPausesMonitoringAndStartRestores(t *testing.T) {
	global := writeActionConfig(t)
	var stdout bytes.Buffer
	app := actionApp(operation.Result{}, nil, &stdout, nil)
	app.Operate = okOperate

	if code := app.Run(context.Background(), []string{"--config", global, "stop", "web"}); code != exitSuccess {
		t.Fatalf("stop exit = %d, want %d", code, exitSuccess)
	}
	rec := readMonitorRecord(t, global, "web")
	if rec.Active || rec.Source != state.SourceCLIManualStop {
		t.Fatalf("record after stop = %+v", rec)
	}
	if op, found := readOperationSettling(t, global, "web"); found {
		t.Fatalf("stop should clear operation settling, got %+v", op)
	}

	if code := app.Run(context.Background(), []string{"--config", global, "start", "web"}); code != exitSuccess {
		t.Fatalf("start exit = %d, want %d", code, exitSuccess)
	}
	rec = readMonitorRecord(t, global, "web")
	if !rec.Active || rec.Source != state.SourceCLI {
		t.Fatalf("record after start = %+v", rec)
	}
	op, found := readOperationSettling(t, global, "web")
	if !found || op.Phase != state.OperationSettlingSettling {
		t.Fatalf("start settling = %+v found=%v", op, found)
	}
}

func TestStopStartPreservesExistingUnmonitor(t *testing.T) {
	global := writeActionConfig(t)
	writeMonitorRecord(t, global, "web", false, state.SourceCLI)
	app := actionApp(operation.Result{}, nil, nil, nil)
	app.Operate = okOperate

	if code := app.Run(context.Background(), []string{"--config", global, "stop", "web"}); code != exitSuccess {
		t.Fatalf("stop exit = %d, want %d", code, exitSuccess)
	}
	if code := app.Run(context.Background(), []string{"--config", global, "start", "web"}); code != exitSuccess {
		t.Fatalf("start exit = %d, want %d", code, exitSuccess)
	}
	rec := readMonitorRecord(t, global, "web")
	if rec.Active || rec.Source != state.SourceCLI {
		t.Fatalf("record after preserved unmonitor = %+v", rec)
	}
}

// writeCascadeConfig sets up a primary `web` that cascades to `db` via
// also_apply, so restart web runs both services through Operate.
func writeCascadeConfig(t *testing.T) string {
	t.Helper()
	global := writeServiceConfig(t, `
paths:
  services: [ @ROOT@/services ]
  runtime: @ROOT@/run
  state: @ROOT@/state
defaults:
  policy:
    cooldown: 5m
`, map[string]string{
		"services/web.yml": `
name: web
service: web
also_apply: [db]
`,
		"services/db.yml": `
name: db
service: db
`,
	})
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

func TestCascadeRetriesBlockedTarget(t *testing.T) {
	global := writeCascadeConfig(t)
	app := actionApp(operation.Result{}, nil, nil, nil)
	calls := map[string]int{}
	app.Operate = func(_ context.Context, _ options, _ *config.Config, _ config.Resolved, service, action string) (operation.Result, error) {
		calls[service]++
		status := operation.ResultOK
		if service == "db" && calls[service] == 1 {
			status = operation.ResultBlocked
		}
		return operation.Result{Service: service, Action: action, Status: status}, nil
	}

	code := app.Run(t.Context(), []string{"--config", global, "restart", "web"})
	if code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if calls["web"] != 1 || calls["db"] != 2 {
		t.Fatalf("calls = %v, want web once and blocked db twice", calls)
	}
}

func TestCascadeResolveErrorDowngradesPrimary(t *testing.T) {
	global := writeServiceConfig(t, `
paths:
  services: [ @ROOT@/services ]
  runtime: @ROOT@/run
  state: @ROOT@/state
defaults:
  policy:
    cooldown: 5m
`, map[string]string{
		"services/web.yml": `
name: web
service: web
also_apply: [db]
`,
		"services/db.yml": `
name: db
service: db
checks:
  unresolved: { type: tcp, host: "${missing}", port: 5432 }
`,
	})
	var stderr bytes.Buffer
	app := actionApp(operation.Result{}, nil, nil, &stderr)
	app.Operate = okOperate

	code := app.Run(t.Context(), []string{"--config", global, "restart", "web"})
	if code != exitRuntimeError {
		t.Fatalf("Run() exit = %d, want %d for unresolved cascade target", code, exitRuntimeError)
	}
	if got := stderr.String(); !strings.Contains(got, "cascade db:") || strings.Contains(got, "skipped") {
		t.Fatalf("stderr = %q, want failed cascade target without skipped", got)
	}
	store := openTestStateStore(t, global)
	defer func() { _ = store.Close() }()
	events, err := store.RecentEvents(10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 1 || events[0].Service != "db" || events[0].Kind != "cascade" || events[0].Status != string(operation.ResultFailed) {
		t.Fatalf("events = %+v, want failed cascade relationship for db", events)
	}
}

func TestRepairDoesNotCascadeAlsoApply(t *testing.T) {
	global := writeCascadeConfig(t)
	app := actionApp(operation.Result{}, nil, nil, nil)
	var calls []string
	app.Operate = func(_ context.Context, _ options, _ *config.Config, _ config.Resolved, service, action string) (operation.Result, error) {
		calls = append(calls, action+" "+service)
		return operation.Result{Service: service, Action: action, Status: operation.ResultOK}, nil
	}

	if code := app.Run(context.Background(), []string{"--config", global, commandRepair, "web"}); code != exitSuccess {
		t.Fatalf("repair exit = %d, want %d", code, exitSuccess)
	}
	if got := strings.Join(calls, ", "); got != "repair web" {
		t.Fatalf("repair calls = %q, want only the named service", got)
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

func TestReloadPreconditionDoesNotOperate(t *testing.T) {
	noReload := false
	for _, tc := range []struct {
		name       string
		manager    fakeManager
		wantStderr []string
	}{
		{"unsupported", fakeManager{supportsReload: &noReload}, []string{"does not support reload", "reload.command"}},
		{"support error", fakeManager{supportsReloadErr: errors.New("init query failed")}, []string{"reload support unavailable", "init query failed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			global := writeActionConfig(t)
			var stderr bytes.Buffer
			called := false
			app := actionApp(operation.Result{Service: "web", Action: "reload", Status: operation.ResultOK}, nil, nil, &stderr)
			app.Detector = fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendOpenRC}}
			app.NewManager = func(servicemgr.Backend) (servicemgr.Manager, error) {
				return tc.manager, nil
			}
			app.Operate = func(context.Context, options, *config.Config, config.Resolved, string, string) (operation.Result, error) {
				called = true
				return operation.Result{}, nil
			}

			code := app.Run(context.Background(), []string{"--config", global, "reload", "web"})
			if code != exitRuntimeError {
				t.Fatalf("Run() exit = %d, want %d", code, exitRuntimeError)
			}
			if called {
				t.Fatal("reload precondition failure called the operation engine")
			}
			got := stderr.String()
			for _, want := range tc.wantStderr {
				if !strings.Contains(got, want) {
					t.Fatalf("stderr = %q, want %q", got, want)
				}
			}
		})
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

func TestReloadNativeCommandUsesAppRunner(t *testing.T) {
	global := writeReloadCommandConfig(t)
	runner := &execxtest.Runner{}
	var actions []string
	var stdout bytes.Buffer
	noReload := false
	app := App{
		LoadConfig: config.Load,
		Detector:   fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendOpenRC}},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			return fakeManager{actions: &actions, supportsReload: &noReload}, nil
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
	if !runner.Ran("reload-web") {
		t.Fatalf("native reload command did not use App.Runner; calls=%v", runner.Lines())
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

type countingDetector struct {
	calls     *int
	detection servicemgr.Detection
}

func (d countingDetector) Detect(context.Context, servicemgr.Backend) (servicemgr.Detection, error) {
	*d.calls++
	return d.detection, nil
}

func TestReloadPreparesBackendOnce(t *testing.T) {
	global := writeActionConfig(t)
	detectCalls := 0
	managerCalls := 0
	var actions []string
	app := App{
		Detector: countingDetector{calls: &detectCalls, detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			managerCalls++
			return fakeManager{
				actions: &actions,
				status:  servicemgr.ServiceStatus{Status: servicemgr.StatusActive},
			}, nil
		},
		Runner: statusUnitRunner{known: "web.service"},
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}

	if code := app.Run(t.Context(), []string{"--config", global, "reload", "web"}); code != exitSuccess {
		t.Fatalf("Run() exit = %d, want %d", code, exitSuccess)
	}
	if detectCalls != 1 || managerCalls != 1 {
		t.Fatalf("Detect calls = %d, NewManager calls = %d; want one prepared backend", detectCalls, managerCalls)
	}
	if len(actions) != 1 || actions[0] != "reload web.service" {
		t.Fatalf("actions = %v, want reload web.service", actions)
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

type deadlineStatusManager struct {
	fakeManager
	hadDeadline *bool
}

func (m deadlineStatusManager) Status(ctx context.Context, service string) (servicemgr.ServiceStatus, error) {
	_, ok := ctx.Deadline()
	*m.hadDeadline = ok
	return m.fakeManager.Status(ctx, service)
}

func TestOperationSessionPostflightStatusReusesPreparedTarget(t *testing.T) {
	global := writeActionConfig(t)
	cfg, err := config.Load(global)
	if err != nil {
		t.Fatal(err)
	}
	resolved, errs := cfg.Resolve("web")
	if len(errs) > 0 {
		t.Fatalf("resolve: %v", errs)
	}
	detectCalls := 0
	managerCalls := 0
	hadDeadline := false
	app := App{
		Detector: countingDetector{calls: &detectCalls, detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			managerCalls++
			return deadlineStatusManager{
				status:      servicemgr.ServiceStatus{Status: servicemgr.StatusActive},
				hadDeadline: &hadDeadline,
			}, nil
		},
		Runner: statusUnitRunner{known: "web.service"},
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}
	session, err := app.newOperationSession(t.Context(), options{config: global}, cfg, nil)
	if err != nil {
		t.Fatalf("newOperationSession: %v", err)
	}
	first, err := session.prepare(t.Context(), "web", resolved)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	second, err := session.prepare(t.Context(), "web", resolved)
	if err != nil {
		t.Fatalf("prepare again: %v", err)
	}
	if first != second {
		t.Fatal("prepare returned two runtimes for one service")
	}
	active := session.activeAfterPostflightFailure(t.Context(), options{}, cfg, resolved, "web", "start",
		operation.Result{Status: operation.ResultPostflightFailed}, nil)
	if !active {
		t.Fatal("activeAfterPostflightFailure = false, want true")
	}
	if detectCalls != 1 || managerCalls != 1 {
		t.Fatalf("Detect calls = %d, NewManager calls = %d; want prepared target reuse", detectCalls, managerCalls)
	}
	if !hadDeadline {
		t.Fatal("postflight status query had no deadline")
	}
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

	_, err = runProductionOperation(context.Background(), app, options{timeout: opTimeout, config: global}, cfg, resolved, "web", "start")
	if err != nil {
		t.Fatalf("production operation: %v", err)
	}

	// The engine must receive the full operation budget, not detectDelay less.
	if remaining < 150*time.Millisecond {
		t.Fatalf("engine context had %v remaining, want ~%v after %v detect delay", remaining, opTimeout, detectDelay)
	}
}

func TestWithDefaultsLeavesProductionOperationToSession(t *testing.T) {
	app := (App{}).withDefaults()
	if app.Operate != nil {
		t.Fatal("withDefaults installed a second production operation path")
	}
}

func TestOperationSessionFallsBackToConfiguredServiceUnit(t *testing.T) {
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
			return fakeManager{actions: &actions, status: servicemgr.ServiceStatus{Status: servicemgr.StatusActive}}, nil
		},
		Runner: statusUnitRunner{},
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
	}

	result, err := runProductionOperation(context.Background(), app, options{config: global}, cfg, resolved, "legacy", "start")
	if err != nil {
		t.Fatalf("production operation: %v", err)
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

func TestOperationSessionUsesCanonicalServiceCheckDependencies(t *testing.T) {
	tests := []struct {
		name      string
		checkType string
	}{
		{name: "stale binaries", checkType: "stale_binary"},
		{name: "strays", checkType: "strays"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			global := writeServiceConfig(t, `
paths:
  services: [ @ROOT@/services ]
  runtime: @ROOT@/run
  state: @ROOT@/state
defaults:
  policy:
    cooldown: 5m
`, map[string]string{
				"services/web.yml": `
name: web
service: web
preflight:
  process-safety:
    type: ` + tc.checkType + `
`,
			})
			cfg, err := config.Load(global)
			if err != nil {
				t.Fatal(err)
			}
			resolved, errs := cfg.Resolve("web")
			if len(errs) > 0 {
				t.Fatalf("resolve: %v", errs)
			}

			var actions []string
			app := App{
				Detector: fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
				NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
					return fakeManager{actions: &actions, status: servicemgr.ServiceStatus{Status: servicemgr.StatusActive}}, nil
				},
				Stdout: &bytes.Buffer{},
				Stderr: &bytes.Buffer{},
			}
			result, err := runProductionOperation(t.Context(), app, options{config: global}, cfg, resolved, "web", "start")
			if err != nil {
				t.Fatalf("production operation: %v", err)
			}
			if result.Status != operation.ResultOK {
				t.Fatalf("result = %+v, want ok with an empty %s preflight", result, tc.checkType)
			}
			if len(actions) != 1 || actions[0] != "start web.service" {
				t.Fatalf("actions = %v, want start web.service", actions)
			}
		})
	}
}

func TestPreflightUsesCanonicalServiceCheckDependencies(t *testing.T) {
	tests := []struct {
		name      string
		checkType string
	}{
		{name: "stale binaries", checkType: "stale_binary"},
		{name: "strays", checkType: "strays"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			global := writeServiceConfig(t, `
paths:
  services: [ @ROOT@/services ]
  runtime: @ROOT@/run
  state: @ROOT@/state
defaults:
  policy:
    cooldown: 5m
`, map[string]string{
				"services/web.yml": `
name: web
service: web
preflight:
  process-safety:
    type: ` + tc.checkType + `
`,
			})
			var stdout bytes.Buffer
			app := App{
				Detector: fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
				NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
					return fakeManager{status: servicemgr.ServiceStatus{Status: servicemgr.StatusActive}}, nil
				},
				Runner: statusUnitRunner{known: "web.service"},
				Stdout: &stdout,
				Stderr: &bytes.Buffer{},
			}
			if code := app.Run(t.Context(), []string{"--config", global, "preflight", "web"}); code != exitSuccess {
				t.Fatalf("Run() exit = %d, want %d; output=%s", code, exitSuccess, stdout.String())
			}
		})
	}
}

func TestOperationSessionPersistsOneOperationEvent(t *testing.T) {
	global := writeActionConfig(t)
	cfg, err := config.Load(global)
	if err != nil {
		t.Fatal(err)
	}
	resolved, errs := cfg.Resolve("web")
	if len(errs) > 0 {
		t.Fatalf("resolve: %v", errs)
	}

	var actions []string
	app := App{
		Detector: fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			return fakeManager{actions: &actions, status: servicemgr.ServiceStatus{Status: servicemgr.StatusActive}}, nil
		},
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}
	result, err := runProductionOperation(context.Background(), app, options{config: global}, cfg, resolved, "web", "restart")
	if err != nil {
		t.Fatalf("production operation: %v", err)
	}
	if result.Status != operation.ResultOK {
		t.Fatalf("result = %+v, want ok", result)
	}

	store := openTestStateStore(t, global)
	defer func() { _ = store.Close() }()
	events, err := store.RecentEvents(10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want exactly one", events)
	}
	want := events[0]
	if want.Service != "web" || want.Kind != "action" || want.Action != "restart" || want.Status != string(operation.ResultOK) {
		t.Fatalf("event = %+v", want)
	}
}

func TestOperationSessionDoesNotActWithoutEventStore(t *testing.T) {
	global := writeActionConfig(t)
	cfg, err := config.Load(global)
	if err != nil {
		t.Fatal(err)
	}
	resolved, errs := cfg.Resolve("web")
	if len(errs) > 0 {
		t.Fatalf("resolve: %v", errs)
	}
	if err := os.WriteFile(cfg.Global.StateDir(), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("block state directory: %v", err)
	}

	var actions []string
	app := App{
		Detector: fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			return fakeManager{actions: &actions}, nil
		},
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}
	_, err = runProductionOperation(context.Background(), app, options{config: global}, cfg, resolved, "web", "restart")
	if err == nil || !strings.Contains(err.Error(), "operation event store unavailable") {
		t.Fatalf("production operation error = %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("actions = %v, want none", actions)
	}
}

func runProductionOperation(ctx context.Context, app App, opts options, cfg *config.Config, resolved config.Resolved, service, action string) (operation.Result, error) {
	runner, closeRunner, err := app.prepareManualOperationRunner(ctx, opts, cfg, resolved, service, action, nil)
	if err != nil {
		return operation.Result{}, err
	}
	defer closeRunner()
	return runner.operate(ctx, opts, cfg, resolved, service, action)
}

type flakyRecorder struct {
	failures int
	calls    int
	records  []state.EventRecord
	err      error
}

func (f *flakyRecorder) RecordEvent(e state.EventRecord) (int64, error) {
	f.calls++
	if f.calls <= f.failures {
		if f.err != nil {
			return 0, f.err
		}
		return 0, sqliteTestError(sqlite3.SQLITE_BUSY)
	}
	f.records = append(f.records, e)
	return int64(f.calls), nil
}

type sqliteTestError int

func (e sqliteTestError) Error() string { return "sqlite test error" }
func (e sqliteTestError) Code() int     { return int(e) }

// TestRecordManualActionEventRetriesThroughContention covers the loaded-host
// case where sermod holds the SQLite writer lock longer than one busy-timeout
// window: the manual audit write must retry instead of failing the completed
// operation on the first SQLITE_BUSY.
func TestRecordManualActionEventRetriesThroughContention(t *testing.T) {
	rec := &flakyRecorder{failures: 2}
	err := recordManualActionEvent(context.Background(), rec, state.EventRecord{Service: "acpid", Action: "stop"})
	if err != nil {
		t.Fatalf("recordManualActionEvent() error = %v, want nil after retries", err)
	}
	if rec.calls != 3 || len(rec.records) != 1 {
		t.Fatalf("calls = %d, records = %d; want 3 calls and 1 record", rec.calls, len(rec.records))
	}
}

func TestRecordManualActionEventStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := &flakyRecorder{failures: 1000}
	err := recordManualActionEvent(ctx, rec, state.EventRecord{Service: "acpid", Action: "stop"})
	if err == nil {
		t.Fatal("recordManualActionEvent() = nil, want error when the context is cancelled")
	}
	if rec.calls != 1 {
		t.Fatalf("calls = %d, want exactly one attempt under a cancelled context", rec.calls)
	}
}

func TestRecordManualActionEventDoesNotRetryPermanentFailure(t *testing.T) {
	rec := &flakyRecorder{failures: 1000, err: sqliteTestError(sqlite3.SQLITE_READONLY)}
	err := recordManualActionEvent(context.Background(), rec, state.EventRecord{Service: "acpid", Action: "stop"})
	if err == nil {
		t.Fatal("recordManualActionEvent() = nil, want permanent error")
	}
	if rec.calls != 1 {
		t.Fatalf("calls = %d, want exactly one attempt for a permanent error", rec.calls)
	}
}
