// Package execx runs external commands with timeout and output handling.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// Result contains the observable result of an external command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

// Runner executes external commands. Callers must pass a context with a timeout
// (or use the package Run helper below).
//
// Sermo prefers native Go (stdlib, x/sys, x/net) over spawning processes (see
// AGENTS.md "Native Go, not external processes"). This runner exists only for the
// few justified cases: the service-manager backends (systemctl/rc-service, which
// have no native API), user-configured `command` checks and watch hooks, and the
// `libraries` check's `ldd`. It always invokes an argv directly — never a shell —
// so check/hook commands cannot be subject to shell injection.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (Result, error)
}

// CommandRunner executes commands through os/exec (argv only, no shell).
type CommandRunner struct{}

// Run executes name with args and captures stdout/stderr.
func (CommandRunner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, name, args...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: 0,
		Duration: time.Since(start),
	}

	if err == nil {
		return result, nil
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		result.ExitCode = -1
		return result, fmt.Errorf("run %s: %w", name, ctxErr)
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, fmt.Errorf("run %s: exit code %d", name, result.ExitCode)
	}

	result.ExitCode = -1
	return result, fmt.Errorf("run %s: %w", name, err)
}

// CommandLookup finds executable commands.
type CommandLookup interface {
	LookPath(name string) (string, error)
}

// OSLookup finds commands using the current PATH.
type OSLookup struct{}

// LookPath implements CommandLookup.
func (OSLookup) LookPath(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("look up %s: %w", name, err)
	}
	return path, nil
}

// WithTimeout is a convenience wrapper around context.WithTimeout.
//
// It is the recommended way to prepare a context before calling Runner.Run
// when you have a per-command timeout value. Every external command executed
// via execx **must** have a deadline (see AGENTS.md).
func WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

// Run is a fortified wrapper that ensures the command runs under a deadline
// and then delegates to r.Run.
//
// Typical usage:
//
//	res, err := execx.Run(ctx, runner, 5*time.Second, "systemctl", "is-active", unit)
//
// If timeout <= 0 the parent's deadline (if any) is used as-is. Callers are
// still encouraged to pass a positive per-command timeout for fast-failing
// probes and queries.
func Run(ctx context.Context, r Runner, timeout time.Duration, name string, args ...string) (Result, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return r.Run(ctx, name, args...)
}
