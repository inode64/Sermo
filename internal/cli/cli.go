package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"sermo/internal/servicemgr"
)

const (
	exitSuccess      = 0
	exitNotActive    = 1
	exitRuntimeError = 2
	exitUsage        = 64
)

// BackendDetector detects the service manager backend.
type BackendDetector interface {
	Detect(ctx context.Context, requested servicemgr.Backend) (servicemgr.Detection, error)
}

// App contains dependencies for the sermoctl CLI.
type App struct {
	Detector   BackendDetector
	NewManager func(servicemgr.Backend) (servicemgr.Manager, error)
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
	command string
	service string
}

// Main runs sermoctl using process IO.
func Main(ctx context.Context, args []string) int {
	app := App{
		Detector:   servicemgr.NewDetector(),
		NewManager: servicemgr.NewManager,
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
	if opts.service == "" {
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
	if opts.service == "" {
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
	if opts.service == "" {
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
		actErr = manager.Start(ctx, opts.service)
	case "stop":
		actErr = manager.Stop(ctx, opts.service)
	case "restart":
		actErr = manager.Restart(ctx, opts.service)
	}
	if actErr != nil {
		a.reportError(opts, fmt.Sprintf("%s failed: %v", action, actErr))
		return exitRuntimeError
	}

	// Verify and report the state the action left the service in.
	status, err := manager.Status(ctx, opts.service)
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

	status, err := manager.Status(ctx, opts.service)
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
		case strings.HasPrefix(arg, "-"):
			return opts, fmt.Errorf("unknown flag %s", arg)
		case opts.command == "":
			opts.command = arg
		case opts.service == "":
			opts.service = arg
		default:
			return opts, fmt.Errorf("unexpected argument %q", arg)
		}
	}
	return opts, nil
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: sermoctl [--backend auto|systemd|openrc] [--json] [--quiet] [--timeout duration] COMMAND [SERVICE]")
	fmt.Fprintln(w, "commands: backend | status SERVICE | is-active SERVICE | start SERVICE | stop SERVICE | restart SERVICE")
}

func writeJSON(w io.Writer, value any) {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}
