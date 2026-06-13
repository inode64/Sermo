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
	return runPrepared(ctx, cmd, start, name)
}

// RunEnv is like Run, but allows providing a completely custom environment
// (instead of inheriting the current process environment). If env is nil or
// empty, it behaves like Run (inherits os.Environ).
func (CommandRunner) RunEnv(ctx context.Context, env []string, name string, args ...string) (Result, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = env
	}
	return runPrepared(ctx, cmd, start, name)
}

// runPrepared executes a ready-to-run *exec.Cmd (with Stdout/Stderr and Env
// already configured by the caller) and maps the result/error in the
// standard execx way. The ctx passed must be the same one given to
// CommandContext so that we can detect deadline errors.
func runPrepared(ctx context.Context, cmd *exec.Cmd, start time.Time, displayName string) (Result, error) {
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
		return result, fmt.Errorf("run %s: %w", displayName, ctxErr)
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, fmt.Errorf("run %s: exit code %d", displayName, result.ExitCode)
	}

	result.ExitCode = -1
	return result, fmt.Errorf("run %s: %w", displayName, err)
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

// deadline returns a derived context with timeout applied when > 0.
// It always returns a cancel func safe to defer-call (no-op when no new
// deadline is added). Centralizes the WithTimeout pattern used by Run/RunEnv.
func deadline(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return ctx, func() {}
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
	ctx, cancel := deadline(ctx, timeout)
	defer cancel()
	return r.Run(ctx, name, args...)
}

// EnvRunner is an optional interface implemented by runners that can execute
// commands with a caller-supplied environment (instead of always inheriting
// the current process environment). It is primarily used by hook execution.
type EnvRunner interface {
	Runner
	RunEnv(ctx context.Context, env []string, name string, args ...string) (Result, error)
}

// RunEnv is the fortified equivalent of Run for cases that need a custom
// environment (e.g. hooks that inject SERMO_* variables).
//
// It applies the timeout (if > 0) and then delegates to an EnvRunner if the
// provided runner implements it. If the runner does not implement EnvRunner,
// it returns an error (in normal Sermo usage we always pass CommandRunner
// for hooks).
func RunEnv(ctx context.Context, r Runner, env []string, timeout time.Duration, name string, args ...string) (Result, error) {
	ctx, cancel := deadline(ctx, timeout)
	defer cancel()

	if er, ok := r.(EnvRunner); ok {
		return er.RunEnv(ctx, env, name, args...)
	}
	return Result{}, fmt.Errorf("execx: runner does not support custom environment (got %T)", r)
}
