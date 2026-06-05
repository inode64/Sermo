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
	exitRuntimeError = 2
	exitUsage        = 64
)

// BackendDetector detects the service manager backend.
type BackendDetector interface {
	Detect(ctx context.Context, requested servicemgr.Backend) (servicemgr.Detection, error)
}

// App contains dependencies for the sermoctl CLI.
type App struct {
	Detector BackendDetector
	Env      func(string) string
	Stdout   io.Writer
	Stderr   io.Writer
}

type options struct {
	backend servicemgr.Backend
	json    bool
	help    bool
	timeout time.Duration
	command string
}

// Main runs sermoctl using process IO.
func Main(ctx context.Context, args []string) int {
	app := App{
		Detector: servicemgr.NewDetector(),
		Env:      os.Getenv,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
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
		opts.timeout = 2 * time.Second
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

func parseArgs(args []string) (options, error) {
	opts := options{backend: ""}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			opts.help = true
		case arg == "--json":
			opts.json = true
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
		default:
			return opts, fmt.Errorf("unexpected argument %q", arg)
		}
	}
	return opts, nil
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: sermoctl [--backend auto|systemd|openrc] [--json] [--timeout duration] backend")
}

func writeJSON(w io.Writer, value any) {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}
