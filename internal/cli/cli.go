package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/locks"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
)

const (
	exitSuccess       = 0
	exitNotActive     = 1
	exitRuntimeError  = 2
	exitUsage         = 64
	exitBlocked       = 75
	exitConfigInvalid = 78
)

// BackendDetector detects the service manager backend.
type BackendDetector interface {
	Detect(ctx context.Context, requested servicemgr.Backend) (servicemgr.Detection, error)
}

// App contains dependencies for the sermoctl CLI.
type App struct {
	Detector   BackendDetector
	NewManager func(servicemgr.Backend) (servicemgr.Manager, error)
	LoadConfig func(globalPath string) (*config.Config, error)
	Discover   func(selectors []process.Selector) ([]process.Process, []string)
	// Operate runs a start/stop/restart through the operation engine for a
	// resolved service. Injectable for testing; the error covers backend/wiring
	// failures (the Result carries operational outcomes).
	Operate func(ctx context.Context, opts options, cfg *config.Config, resolved config.Resolved, service, action string) (operation.Result, error)
	Env     func(string) string
	Stdout  io.Writer
	Stderr  io.Writer
	// Runner executes external commands (e.g. an app's version command for the
	// `apps` command). Injectable for testing; defaults to the real runner.
	Runner execx.Runner
}

type options struct {
	backend servicemgr.Backend
	json    bool
	quiet   bool
	help    bool
	timeout time.Duration
	config  string
	command string
	args    []string
	// lock command flags
	name        string
	reason      string
	ttl         time.Duration
	commandArgs []string // tokens after `--`
}

// service returns the first positional argument after the command.
func (o options) service() string {
	if len(o.args) == 0 {
		return ""
	}
	return o.args[0]
}

// Main runs sermoctl using process IO.
func Main(ctx context.Context, args []string) int {
	app := App{
		Detector:   servicemgr.NewDetector(),
		NewManager: servicemgr.NewManager,
		LoadConfig: config.Load,
		Discover:   process.NewDiscoverer().Discover,
		Env:        os.Getenv,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	}
	app.Operate = app.defaultOperate
	return app.Run(ctx, args)
}

// Run executes the CLI.
func (a App) Run(ctx context.Context, args []string) int {
	if a.Env == nil {
		a.Env = os.Getenv
	}
	if a.Stdout == nil {
		a.Stdout = io.Discard
	}
	if a.Stderr == nil {
		a.Stderr = io.Discard
	}
	if a.Detector == nil {
		a.Detector = servicemgr.NewDetector()
	}
	if a.NewManager == nil {
		a.NewManager = servicemgr.NewManager
	}
	if a.LoadConfig == nil {
		a.LoadConfig = config.Load
	}
	if a.Discover == nil {
		a.Discover = process.NewDiscoverer().Discover
	}
	if a.Operate == nil {
		a.Operate = a.defaultOperate
	}
	if a.Runner == nil {
		a.Runner = execx.CommandRunner{}
	}

	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(a.Stderr, "usage error: %v\n", err)
		writeUsage(a.Stderr)
		return exitUsage
	}
	if opts.help {
		writeUsage(a.Stdout)
		return exitSuccess
	}
	if opts.timeout <= 0 {
		opts.timeout = defaultTimeout(opts.command)
	}
	if opts.backend == "" {
		envBackend, err := servicemgr.ParseBackend(a.Env("SERMO_BACKEND"))
		if err != nil {
			fmt.Fprintf(a.Stderr, "usage error: SERMO_BACKEND: %v\n", err)
			return exitUsage
		}
		opts.backend = envBackend
	}

	switch opts.command {
	case "backend", "init":
		return a.runBackend(ctx, opts)
	case "status":
		return a.runStatus(ctx, opts)
	case "is-active":
		return a.runIsActive(ctx, opts)
	case "start", "stop", "restart":
		return a.runAction(ctx, opts, opts.command)
	case "config":
		return a.runConfig(opts)
	case "locks":
		return a.runLocks(opts)
	case "processes":
		return a.runProcesses(opts)
	case "preflight":
		return a.runPreflight(ctx, opts)
	case "profile":
		return a.runProfile(opts)
	case "apps":
		return a.runApps(ctx, opts)
	case "libs":
		return a.runLibs(ctx, opts)
	case "services":
		return a.runServices(ctx, opts)
	case "service":
		return a.runService(opts)
	case "lock":
		return a.runLock(ctx, opts)
	case "unmonitor":
		return a.runMonitor(opts, true)
	case "monitor":
		return a.runMonitor(opts, false)
	case "":
		fmt.Fprintln(a.Stderr, "usage error: missing command")
		writeUsage(a.Stderr)
		return exitUsage
	default:
		fmt.Fprintf(a.Stderr, "usage error: unknown command %q\n", opts.command)
		writeUsage(a.Stderr)
		return exitUsage
	}
}

func (a App) runBackend(ctx context.Context, opts options) int {
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		if opts.json {
			writeJSON(a.Stdout, map[string]string{"error": err.Error()})
		} else {
			fmt.Fprintf(a.Stderr, "backend detection failed: %v\n", err)
		}
		return exitRuntimeError
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]string{"backend": string(detection.Backend)})
		return exitSuccess
	}

	fmt.Fprintln(a.Stdout, detection.Backend)
	return exitSuccess
}

func (a App) runStatus(ctx context.Context, opts options) int {
	if opts.service() == "" {
		fmt.Fprintln(a.Stderr, "usage error: status requires a service name")
		writeUsage(a.Stderr)
		return exitUsage
	}

	status, code := a.serviceStatus(ctx, opts)
	if code != exitSuccess {
		return code
	}

	paused := a.servicePaused(opts)
	if opts.json {
		writeJSON(a.Stdout, statusToJSON(status, paused))
		return exitSuccess
	}

	monitoring := ""
	if paused {
		monitoring = " monitoring=paused"
	}
	fmt.Fprintf(a.Stdout, "%s %s backend=%s service=%s%s\n",
		status.Service, status.Status, status.Backend, status.Unit, monitoring)
	return exitSuccess
}

// servicePaused reports whether monitoring is paused for the requested service.
// It is best-effort: status works without config, so a config that fails to load
// simply yields false.
func (a App) servicePaused(opts options) bool {
	globalPath := opts.config
	if globalPath == "" {
		globalPath = config.DefaultGlobalPath
	}
	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		return false
	}
	store, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		return false
	}
	defer store.Close()
	active, found, err := store.Active(opts.service())
	return err == nil && found && !active
}

func (a App) runIsActive(ctx context.Context, opts options) int {
	if opts.service() == "" {
		fmt.Fprintln(a.Stderr, "usage error: is-active requires a service name")
		writeUsage(a.Stderr)
		return exitUsage
	}

	status, code := a.serviceStatus(ctx, opts)
	if code != exitSuccess {
		return code
	}

	switch {
	case opts.json:
		writeJSON(a.Stdout, statusToJSON(status, a.servicePaused(opts)))
	case !opts.quiet:
		fmt.Fprintln(a.Stdout, status.Status)
	}

	if status.Status == servicemgr.StatusActive {
		return exitSuccess
	}
	return exitNotActive
}

// runAction performs a start/stop/restart through the safe operation engine
// (section 18): the resolved service is run under the internal operation lock,
// active named runtime locks, required preflight, guards, residual-process
// handling and postflight. Manual sermoctl actions are not rate limited, but are
// fully guarded (section 16).
func (a App) runAction(ctx context.Context, opts options, action string) int {
	if opts.service() == "" {
		fmt.Fprintf(a.Stderr, "usage error: %s requires a service name\n", action)
		writeUsage(a.Stderr)
		return exitUsage
	}
	service := opts.service()

	globalPath := opts.config
	if globalPath == "" {
		globalPath = config.DefaultGlobalPath
	}
	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("load config failed: %v", err))
		return exitRuntimeError
	}
	if _, ok := cfg.Services[service]; !ok {
		a.reportError(opts, fmt.Sprintf("unknown service %q", service))
		return exitRuntimeError
	}
	resolved, errs := cfg.Resolve(service)
	if len(errs) > 0 {
		a.printIssues(opts, scopedIssues(service, errs))
		return exitConfigInvalid
	}

	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	result, err := a.Operate(ctx, opts, cfg, resolved, service, action)
	if err != nil {
		a.reportError(opts, err.Error())
		return exitRuntimeError
	}

	if opts.json {
		writeJSON(a.Stdout, result)
	} else if !opts.quiet {
		a.printOperation(result)
	}
	return operationExit(result.Status)
}

// defaultOperate wires the real operation engine from a resolved service and
// runs the requested action.
func (a App) defaultOperate(ctx context.Context, opts options, cfg *config.Config, resolved config.Resolved, service, action string) (operation.Result, error) {
	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		return operation.Result{}, fmt.Errorf("backend detection failed: %v", err)
	}
	manager, err := a.NewManager(detection.Backend)
	if err != nil {
		return operation.Result{}, fmt.Errorf("service manager unavailable: %v", err)
	}

	base := config.ServiceUnit(resolved.Tree, service)
	aliases := config.UnitAliases(resolved.Tree, string(detection.Backend))
	unit, err := servicemgr.NewUnitResolver().Resolve(ctx, detection.Backend, base, aliases)
	if err != nil {
		return operation.Result{}, err
	}

	runtime := cfg.Global.RuntimeDir()
	locker := locks.NewOperationLocker(filepath.Join(runtime, "ops"))
	discoverer := process.NewDiscoverer()
	discoverer.BackendPIDs = servicemgr.BackendPIDsFunc(detection.Backend, unit)
	engine := operation.New(operation.Config{
		Service:    service,
		Unit:       unit,
		Backend:    string(detection.Backend),
		Tree:       resolved.Tree,
		Manager:    manager,
		Locker:     &locker,
		Scanner:    locks.NewScanner(filepath.Join(runtime, "locks")),
		Discoverer: discoverer,
		CheckDeps:  checks.Deps{DefaultTimeout: engineDefaultTimeout(cfg)},
	})

	switch action {
	case "start":
		return engine.Start(ctx), nil
	case "stop":
		return engine.Stop(ctx), nil
	case "restart":
		return engine.Restart(ctx), nil
	default:
		return operation.Result{}, fmt.Errorf("unknown action %q", action)
	}
}

func (a App) printOperation(r operation.Result) {
	switch r.Status {
	case operation.ResultOK:
		fmt.Fprintf(a.Stdout, "%s %s ok\n", r.Service, r.Action)
	case operation.ResultBlocked:
		fmt.Fprintf(a.Stdout, "BLOCKED %s %s\n", r.Service, r.Action)
		if r.Message != "" {
			fmt.Fprintf(a.Stdout, "reason: %s\n", r.Message)
		}
	default:
		fmt.Fprintf(a.Stdout, "%s %s %s\n", r.Service, r.Action, r.Status)
		if r.Message != "" {
			fmt.Fprintf(a.Stdout, "reason: %s\n", r.Message)
		}
	}
	for _, c := range r.Checks {
		if !c.OK {
			fmt.Fprintf(a.Stdout, "  check %s failed: %s\n", c.Check, c.Message)
		}
	}
	for _, p := range r.Processes {
		exe := p.Exe
		if !p.ExeOK {
			exe = "unknown"
		}
		fmt.Fprintf(a.Stdout, "  residual pid=%d exe=%s\n", p.PID, exe)
	}
}

// operationExit maps an operation result status to a process exit code (§23).
func operationExit(status operation.ResultStatus) int {
	switch status {
	case operation.ResultOK:
		return exitSuccess
	case operation.ResultBlocked:
		return exitBlocked
	case operation.ResultFailed:
		return exitRuntimeError
	default: // preflight_failed, postflight_failed, orphan_processes
		return exitNotActive
	}
}

// runConfig dispatches the `config` subcommands (validate, render).
func (a App) runConfig(opts options) int {
	if len(opts.args) == 0 {
		fmt.Fprintln(a.Stderr, "usage error: config requires a subcommand (validate|render)")
		writeUsage(a.Stderr)
		return exitUsage
	}

	sub := opts.args[0]
	rest := opts.args[1:]
	globalPath := opts.config
	if globalPath == "" {
		globalPath = config.DefaultGlobalPath
	}

	switch sub {
	case "render":
		return a.runConfigRender(globalPath, rest, opts)
	case "validate":
		return a.runConfigValidate(globalPath, rest, opts)
	case "diff":
		return a.runConfigDiff(globalPath, rest, opts)
	default:
		fmt.Fprintf(a.Stderr, "usage error: unknown config subcommand %q\n", sub)
		writeUsage(a.Stderr)
		return exitUsage
	}
}

func (a App) runConfigRender(globalPath string, rest []string, opts options) int {
	if len(rest) == 0 {
		fmt.Fprintln(a.Stderr, "usage error: config render requires a service name")
		return exitUsage
	}
	service := rest[0]

	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("load config failed: %v", err))
		return exitRuntimeError
	}
	if _, ok := cfg.Services[service]; !ok {
		a.reportError(opts, fmt.Sprintf("unknown service %q", service))
		return exitRuntimeError
	}

	resolved, errs := cfg.Resolve(service)
	if len(errs) > 0 {
		a.printIssues(opts, scopedIssues(service, errs))
		return exitConfigInvalid
	}

	var out []byte
	if opts.json {
		out, err = config.RenderJSON(resolved)
	} else {
		out, err = config.RenderYAML(resolved)
	}
	if err != nil {
		a.reportError(opts, fmt.Sprintf("render failed: %v", err))
		return exitRuntimeError
	}

	_, _ = a.Stdout.Write(out)
	if n := len(out); n == 0 || out[n-1] != '\n' {
		fmt.Fprintln(a.Stdout)
	}
	return exitSuccess
}

func (a App) runConfigValidate(globalPath string, rest []string, opts options) int {
	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("load config failed: %v", err))
		return exitRuntimeError
	}

	issues := config.Validate(cfg)
	if len(rest) > 0 {
		issues = filterIssues(issues, rest[0])
	}

	if len(issues) == 0 {
		switch {
		case opts.json:
			writeJSON(a.Stdout, map[string]any{"valid": true})
		case !opts.quiet:
			fmt.Fprintln(a.Stdout, "OK")
		}
		return exitSuccess
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{"valid": false, "errors": issuesJSON(issues)})
	} else {
		a.printIssues(opts, issues)
	}
	return exitConfigInvalid
}

// printIssues writes validation findings in the section-30 ERROR format.
func (a App) printIssues(opts options, issues []config.Issue) {
	if opts.json {
		writeJSON(a.Stdout, map[string]any{"valid": false, "errors": issuesJSON(issues)})
		return
	}
	for _, is := range issues {
		fmt.Fprintf(a.Stderr, "ERROR %s:\n  %s\n", is.Scope, is.Msg)
	}
}

func scopedIssues(scope string, msgs []string) []config.Issue {
	issues := make([]config.Issue, 0, len(msgs))
	for _, m := range msgs {
		issues = append(issues, config.Issue{Scope: scope, Msg: m})
	}
	return issues
}

func filterIssues(issues []config.Issue, scope string) []config.Issue {
	out := make([]config.Issue, 0, len(issues))
	for _, is := range issues {
		if is.Scope == scope {
			out = append(out, is)
		}
	}
	return out
}

func issuesJSON(issues []config.Issue) []map[string]string {
	out := make([]map[string]string, 0, len(issues))
	for _, is := range issues {
		out = append(out, map[string]string{"scope": is.Scope, "error": is.Msg})
	}
	return out
}

// runPreflight resolves a service, builds its preflight checks and runs them
// under engine.default_timeout (section 19). A required check failure exits 1.
func (a App) runPreflight(ctx context.Context, opts options) int {
	if opts.service() == "" {
		fmt.Fprintln(a.Stderr, "usage error: preflight requires a service name")
		writeUsage(a.Stderr)
		return exitUsage
	}
	service := opts.service()

	globalPath := opts.config
	if globalPath == "" {
		globalPath = config.DefaultGlobalPath
	}
	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("load config failed: %v", err))
		return exitRuntimeError
	}
	if _, ok := cfg.Services[service]; !ok {
		a.reportError(opts, fmt.Sprintf("unknown service %q", service))
		return exitRuntimeError
	}

	resolved, errs := cfg.Resolve(service)
	if len(errs) > 0 {
		a.printIssues(opts, scopedIssues(service, errs))
		return exitConfigInvalid
	}

	section, _ := resolved.Tree["preflight"].(map[string]any)
	deps := checks.Deps{
		Service:        service,
		DefaultTimeout: engineDefaultTimeout(cfg),
		Status:         a.statusFunc(opts, resolved.Tree, config.ServiceUnit(resolved.Tree, service)),
		Processes:      process.NewDiscoverer().ObserveState,
	}
	built, warnings := checks.Build(section, deps)
	for _, w := range warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", w)
	}

	ctx, cancel := context.WithTimeout(ctx, preflightDeadline(deps.DefaultTimeout))
	defer cancel()
	results := checks.Run(ctx, built, 0)
	outcome := checks.Evaluate(results)

	if opts.json {
		writeJSON(a.Stdout, map[string]any{"service": service, "ok": outcome.OK, "checks": results})
	} else {
		a.printPreflight(service, outcome)
	}

	if outcome.OK {
		return exitSuccess
	}
	return exitNotActive
}

func (a App) printPreflight(service string, outcome checks.Outcome) {
	overall := "OK"
	if !outcome.OK {
		overall = "FAIL"
	}
	if len(outcome.Results) == 0 {
		fmt.Fprintf(a.Stdout, "preflight %s: %s (no checks)\n", service, overall)
		return
	}
	fmt.Fprintf(a.Stdout, "preflight %s: %s\n", service, overall)
	for _, r := range outcome.Results {
		tag := "OK"
		if !r.OK {
			tag = "FAIL"
			if r.Optional {
				tag = "WARN"
			}
		}
		fmt.Fprintf(a.Stdout, "  %-4s %s: %s\n", tag, r.Check, r.Message)
	}
}

// statusFunc builds a lazy backend status query for `service` checks; it only
// detects the backend and resolves the unit (aliases, section 11) when a service
// check actually runs.
func (a App) statusFunc(opts options, tree map[string]any, base string) func(context.Context) (servicemgr.Status, error) {
	return func(ctx context.Context) (servicemgr.Status, error) {
		detection, err := a.Detector.Detect(ctx, opts.backend)
		if err != nil {
			return "", err
		}
		manager, err := a.NewManager(detection.Backend)
		if err != nil {
			return "", err
		}
		aliases := config.UnitAliases(tree, string(detection.Backend))
		unit, err := servicemgr.NewUnitResolver().Resolve(ctx, detection.Backend, base, aliases)
		if err != nil {
			return "", err
		}
		status, err := manager.Status(ctx, unit)
		if err != nil {
			return "", err
		}
		return status.Status, nil
	}
}

func engineDefaultTimeout(cfg *config.Config) time.Duration {
	if engine, ok := cfg.Global.Raw["engine"].(map[string]any); ok {
		if s, _ := engine["default_timeout"].(string); s != "" {
			if d, err := time.ParseDuration(s); err == nil && d > 0 {
				return d
			}
		}
	}
	return 10 * time.Second
}

// preflightDeadline bounds the whole run generously above a single check's
// timeout so concurrent checks each get their full per-check budget.
func preflightDeadline(perCheck time.Duration) time.Duration {
	if perCheck <= 0 {
		perCheck = 10 * time.Second
	}
	return perCheck + 5*time.Second
}

// runLocks reports the named runtime locks for a service (active, expired and
// stale), reading the runtime root from the loaded config (section 20).
func (a App) runLocks(opts options) int {
	if opts.service() == "" {
		fmt.Fprintln(a.Stderr, "usage error: locks requires a service name")
		writeUsage(a.Stderr)
		return exitUsage
	}

	globalPath := opts.config
	if globalPath == "" {
		globalPath = config.DefaultGlobalPath
	}
	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("load config failed: %v", err))
		return exitRuntimeError
	}

	dir := filepath.Join(cfg.Global.RuntimeDir(), "locks")
	report, err := locks.NewScanner(dir).Scan(opts.service())
	if err != nil {
		a.reportError(opts, fmt.Sprintf("scan locks failed: %v", err))
		return exitRuntimeError
	}

	for _, w := range report.Warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", w)
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{
			"service": report.Service,
			"locks":   report.Locks,
		})
		return exitSuccess
	}

	if len(report.Locks) == 0 {
		if !opts.quiet {
			fmt.Fprintf(a.Stdout, "no named runtime locks for %s\n", report.Service)
		}
		return exitSuccess
	}
	for _, lock := range report.Locks {
		fmt.Fprintln(a.Stdout, formatLock(lock))
	}
	return exitSuccess
}

func formatLock(lock locks.Lock) string {
	id := lock.Service
	if lock.Name != "" {
		id += "." + lock.Name
	}
	line := fmt.Sprintf("%s %s owner_pid=%d", id, lock.State, lock.OwnerPID)
	if !lock.ExpiresAt.IsZero() {
		line += " expires_at=" + lock.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if lock.StaleReason != "" {
		line += " (" + lock.StaleReason + ")"
	}
	if lock.Reason != "" {
		line += fmt.Sprintf(" reason=%q", lock.Reason)
	}
	return line
}

// runProcesses discovers and reports the processes belonging to a service
// (section 21), reading the service's `processes` selectors from resolved config.
func (a App) runProcesses(opts options) int {
	if opts.service() == "" {
		fmt.Fprintln(a.Stderr, "usage error: processes requires a service name")
		writeUsage(a.Stderr)
		return exitUsage
	}
	service := opts.service()

	globalPath := opts.config
	if globalPath == "" {
		globalPath = config.DefaultGlobalPath
	}
	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("load config failed: %v", err))
		return exitRuntimeError
	}
	if _, ok := cfg.Services[service]; !ok {
		a.reportError(opts, fmt.Sprintf("unknown service %q", service))
		return exitRuntimeError
	}

	resolved, errs := cfg.Resolve(service)
	if len(errs) > 0 {
		a.printIssues(opts, scopedIssues(service, errs))
		return exitConfigInvalid
	}

	selectors, warnings := process.ParseSelectors(resolved.Tree)
	procs, discWarnings := a.Discover(selectors)
	warnings = append(warnings, discWarnings...)

	for _, w := range warnings {
		fmt.Fprintf(a.Stderr, "warning: %s\n", w)
	}

	if opts.json {
		writeJSON(a.Stdout, map[string]any{"service": service, "processes": procs})
		return exitSuccess
	}

	if len(procs) == 0 {
		if !opts.quiet {
			fmt.Fprintf(a.Stdout, "no processes found for %s\n", service)
		}
		return exitSuccess
	}
	for _, p := range procs {
		fmt.Fprintln(a.Stdout, formatProcess(p))
	}
	return exitSuccess
}

func formatProcess(p process.Process) string {
	exe := p.Exe
	if !p.ExeOK {
		exe = "unknown"
	}
	line := fmt.Sprintf("pid=%d ppid=%d user=%s exe=%s source=%s", p.PID, p.PPID, orUnknown(p.User), exe, p.Source)
	if p.Role != "" {
		line += " role=" + p.Role
	}
	return line
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// serviceStatus resolves the backend, builds a manager and queries the service.
// On any failure it reports the error and returns a non-success exit code.
func (a App) serviceStatus(ctx context.Context, opts options) (servicemgr.ServiceStatus, int) {
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("backend detection failed: %v", err))
		return servicemgr.ServiceStatus{}, exitRuntimeError
	}

	manager, err := a.NewManager(detection.Backend)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("service manager unavailable: %v", err))
		return servicemgr.ServiceStatus{}, exitRuntimeError
	}

	status, err := manager.Status(ctx, opts.service())
	if err != nil {
		a.reportError(opts, fmt.Sprintf("status query failed: %v", err))
		return servicemgr.ServiceStatus{}, exitRuntimeError
	}
	return status, exitSuccess
}

func (a App) reportError(opts options, msg string) {
	if opts.json {
		writeJSON(a.Stdout, map[string]string{"error": msg})
		return
	}
	fmt.Fprintln(a.Stderr, msg)
}

type statusJSON struct {
	Service string `json:"service"`
	Backend string `json:"backend"`
	Status  string `json:"status"`
	Unit    string `json:"unit"`
	Paused  bool   `json:"paused"`
}

// defaultTimeout returns the per-command outer deadline used when --timeout is
// not given. Backend actions can legitimately take much longer than a probe.
func defaultTimeout(command string) time.Duration {
	switch command {
	case "start", "stop", "restart":
		return 90 * time.Second
	default:
		return 2 * time.Second
	}
}

func statusToJSON(status servicemgr.ServiceStatus, paused bool) statusJSON {
	return statusJSON{
		Service: status.Service,
		Backend: string(status.Backend),
		Status:  string(status.Status),
		Unit:    status.Unit,
		Paused:  paused,
	}
}

func parseArgs(args []string) (options, error) {
	opts := options{backend: ""}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			opts.help = true
		case arg == "--json":
			opts.json = true
		case arg == "--quiet" || arg == "-q":
			opts.quiet = true
		case strings.HasPrefix(arg, "--backend="):
			backend, err := servicemgr.ParseBackend(strings.TrimPrefix(arg, "--backend="))
			if err != nil {
				return opts, err
			}
			opts.backend = backend
		case arg == "--backend":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("--backend requires a value")
			}
			backend, err := servicemgr.ParseBackend(args[i])
			if err != nil {
				return opts, err
			}
			opts.backend = backend
		case strings.HasPrefix(arg, "--timeout="):
			timeout, err := time.ParseDuration(strings.TrimPrefix(arg, "--timeout="))
			if err != nil {
				return opts, fmt.Errorf("--timeout: %w", err)
			}
			opts.timeout = timeout
		case arg == "--timeout":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("--timeout requires a value")
			}
			timeout, err := time.ParseDuration(args[i])
			if err != nil {
				return opts, fmt.Errorf("--timeout: %w", err)
			}
			opts.timeout = timeout
		case strings.HasPrefix(arg, "--config="):
			opts.config = strings.TrimPrefix(arg, "--config=")
		case arg == "--config":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("--config requires a value")
			}
			opts.config = args[i]
		case strings.HasPrefix(arg, "--name="):
			opts.name = strings.TrimPrefix(arg, "--name=")
		case arg == "--name":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("--name requires a value")
			}
			opts.name = args[i]
		case strings.HasPrefix(arg, "--reason="):
			opts.reason = strings.TrimPrefix(arg, "--reason=")
		case arg == "--reason":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("--reason requires a value")
			}
			opts.reason = args[i]
		case strings.HasPrefix(arg, "--ttl="):
			d, err := time.ParseDuration(strings.TrimPrefix(arg, "--ttl="))
			if err != nil {
				return opts, fmt.Errorf("--ttl: %w", err)
			}
			opts.ttl = d
		case arg == "--ttl":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("--ttl requires a value")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				return opts, fmt.Errorf("--ttl: %w", err)
			}
			opts.ttl = d
		case arg == "--":
			// Everything after `--` is a literal command (the lock wrapper).
			opts.commandArgs = append(opts.commandArgs, args[i+1:]...)
			return opts, nil
		case strings.HasPrefix(arg, "-"):
			return opts, fmt.Errorf("unknown flag %s", arg)
		case opts.command == "":
			opts.command = arg
		default:
			opts.args = append(opts.args, arg)
		}
	}
	return opts, nil
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: sermoctl [--backend auto|systemd|openrc] [--config path] [--json] [--quiet] [--timeout duration] COMMAND [ARGS]")
	fmt.Fprintln(w, "commands: backend | status SERVICE | is-active SERVICE | start SERVICE | stop SERVICE | restart SERVICE")
	fmt.Fprintln(w, "          config validate [SERVICE] | config render SERVICE | config diff BASE SERVICE")
	fmt.Fprintln(w, "          locks SERVICE | processes SERVICE | preflight SERVICE | monitor SERVICE | unmonitor SERVICE")
	fmt.Fprintln(w, "          apps [all] | libs [all] | services [all] | profile list | profile show PROFILE | service list | service show SERVICE")
	fmt.Fprintln(w, "          service clone SOURCE TARGET")
	fmt.Fprintln(w, "          lock SERVICE [--name N] --reason R --ttl D -- COMMAND... | lock acquire ... | lock release SERVICE [--name N]")
}

func writeJSON(w io.Writer, value any) {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}
