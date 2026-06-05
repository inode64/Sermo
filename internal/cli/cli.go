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

	"sermo/internal/config"
	"sermo/internal/locks"
	"sermo/internal/servicemgr"
)

const (
	exitSuccess       = 0
	exitNotActive     = 1
	exitRuntimeError  = 2
	exitUsage         = 64
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
	Env        func(string) string
	Stdout     io.Writer
	Stderr     io.Writer
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
		Env:        os.Getenv,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	}
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

	if opts.json {
		writeJSON(a.Stdout, statusToJSON(status))
		return exitSuccess
	}

	fmt.Fprintf(a.Stdout, "%s %s backend=%s service=%s\n",
		status.Service, status.Status, status.Backend, status.Unit)
	return exitSuccess
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
		writeJSON(a.Stdout, statusToJSON(status))
	case !opts.quiet:
		fmt.Fprintln(a.Stdout, status.Status)
	}

	if status.Status == servicemgr.StatusActive {
		return exitSuccess
	}
	return exitNotActive
}

// runAction performs a raw backend start/stop/restart and reports the resulting
// status. It does not implement the safe operation engine (locks, guards,
// preflight); that wraps these primitives in a later step.
func (a App) runAction(ctx context.Context, opts options, action string) int {
	if opts.service() == "" {
		fmt.Fprintf(a.Stderr, "usage error: %s requires a service name\n", action)
		writeUsage(a.Stderr)
		return exitUsage
	}

	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	detection, err := a.Detector.Detect(ctx, opts.backend)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("backend detection failed: %v", err))
		return exitRuntimeError
	}

	manager, err := a.NewManager(detection.Backend)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("service manager unavailable: %v", err))
		return exitRuntimeError
	}

	var actErr error
	switch action {
	case "start":
		actErr = manager.Start(ctx, opts.service())
	case "stop":
		actErr = manager.Stop(ctx, opts.service())
	case "restart":
		actErr = manager.Restart(ctx, opts.service())
	}
	if actErr != nil {
		a.reportError(opts, fmt.Sprintf("%s failed: %v", action, actErr))
		return exitRuntimeError
	}

	// Verify and report the state the action left the service in.
	status, err := manager.Status(ctx, opts.service())
	if err != nil {
		a.reportError(opts, fmt.Sprintf("status query failed: %v", err))
		return exitRuntimeError
	}

	switch {
	case opts.json:
		writeJSON(a.Stdout, actionJSON{
			Service: status.Service,
			Action:  action,
			Backend: string(status.Backend),
			Status:  string(status.Status),
			Unit:    status.Unit,
		})
	case !opts.quiet:
		fmt.Fprintf(a.Stdout, "%s %s status=%s backend=%s service=%s\n",
			status.Service, action, status.Status, status.Backend, status.Unit)
	}
	return exitSuccess
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
}

type actionJSON struct {
	Service string `json:"service"`
	Action  string `json:"action"`
	Backend string `json:"backend"`
	Status  string `json:"status"`
	Unit    string `json:"unit"`
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

func statusToJSON(status servicemgr.ServiceStatus) statusJSON {
	return statusJSON{
		Service: status.Service,
		Backend: string(status.Backend),
		Status:  string(status.Status),
		Unit:    status.Unit,
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
	fmt.Fprintln(w, "          config validate [SERVICE] | config render SERVICE | locks SERVICE")
}

func writeJSON(w io.Writer, value any) {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}
