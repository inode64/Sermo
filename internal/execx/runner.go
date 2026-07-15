// Package execx runs external commands with timeout and output handling.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// CommandDidNotStart is the operator-facing message when execx marks a run
// failure (ExitCodeRunFailure) but provides no underlying error detail.
const CommandDidNotStart = "command did not start"

const (
	// ExitCodeSuccess is the conventional successful process exit code.
	ExitCodeSuccess = 0
	// ExitCodeRunFailure is the synthetic exit code for start, timeout and runner failures.
	ExitCodeRunFailure = -1
	// NoTimeout disables execx's additional per-command deadline.
	NoTimeout time.Duration = 0
)

const (
	commandRunErrorPrefix        = "run "
	commandRunErrorSeparator     = ": "
	commandRunErrorFormat        = commandRunErrorPrefix + "%s" + commandRunErrorSeparator + "%w"
	commandRunExitCodeFormat     = commandRunErrorPrefix + "%s" + commandRunErrorSeparator + "exit code %d"
	commandRunTimeoutAfterFormat = commandRunErrorPrefix + "%s" + commandRunErrorSeparator + "timeout after %s: %w"
	commandRunTimeoutFormat      = commandRunErrorPrefix + "%s" + commandRunErrorSeparator + "timeout: %w"
	commandWaitDelay             = 2 * time.Second
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
// This runner exists only for the few justified cases where Go has no native
// API (see AGENTS.md "Native by default"): the service-manager backends
// (systemctl/rc-service), user-configured `command` checks and watch hooks,
// `firewall_rules` (iptables-save), and the `libraries` check's `ldd`. It
// always invokes an argv directly — never a shell — so check/hook commands cannot
// be subject to shell injection.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (Result, error)
}

// UserRunner is implemented by runners that can execute a command as a specific
// OS user. The user value is a username or numeric UID resolved on the host.
type UserRunner interface {
	Runner
	RunUser(ctx context.Context, user, name string, args ...string) (Result, error)
}

// CommandRunner executes commands through os/exec (argv only, no shell).
type CommandRunner struct{}

// Run executes name with args and captures stdout/stderr.
func (CommandRunner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, name, args...)
	prepareCommandRuntime(cmd)
	return runPrepared(ctx, cmd, start, name)
}

// RunUser executes name with args as user and captures stdout/stderr.
func (CommandRunner) RunUser(ctx context.Context, user, name string, args ...string) (Result, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, name, args...)
	if err := prepareCommandUser(cmd, user); err != nil {
		return Result{ExitCode: ExitCodeRunFailure, Duration: time.Since(start)}, err
	}
	prepareCommandRuntime(cmd)
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
	prepareCommandRuntime(cmd)
	return runPrepared(ctx, cmd, start, name)
}

func prepareCommandRuntime(cmd *exec.Cmd) {
	prepareCommandProcessGroup(cmd)
	cmd.Cancel = func() error {
		return cancelCommandProcessGroup(cmd)
	}
	cmd.WaitDelay = commandWaitDelay
}

// runPrepared executes a ready-to-run *exec.Cmd (with Stdout/Stderr and Env
// already configured by the caller) and maps the result/error in the standard
// execx way. It returns after the command context is cancelled even when a child
// is stuck in uninterruptible kernel sleep and cannot be reaped immediately.
func runPrepared(ctx context.Context, cmd *exec.Cmd, start time.Time, displayName string) (Result, error) {
	var stdout lockedBuffer
	var stderr lockedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if ctxErr := ctx.Err(); ctxErr != nil {
		result := Result{
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			ExitCode: ExitCodeRunFailure,
			Duration: time.Since(start),
		}
		return result, commandContextError(displayName, result, ctxErr)
	}
	if err := cmd.Start(); err != nil {
		result := Result{
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			ExitCode: ExitCodeRunFailure,
			Duration: time.Since(start),
		}
		return result, fmt.Errorf(commandRunErrorFormat, displayName, err)
	}

	err := waitOrCancel(ctx, cmd)
	result := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: ExitCodeSuccess,
		Duration: time.Since(start),
	}

	if err == nil {
		return result, nil
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		result.ExitCode = ExitCodeRunFailure
		return result, commandContextError(displayName, result, ctxErr)
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		result.ExitCode = exitErr.ExitCode()
		return result, fmt.Errorf(commandRunExitCodeFormat, displayName, result.ExitCode)
	}

	result.ExitCode = ExitCodeRunFailure
	return result, fmt.Errorf(commandRunErrorFormat, displayName, err)
}

func waitOrCancel(ctx context.Context, cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = cancelCommandProcessGroup(cmd)
		timer := time.NewTimer(commandWaitDelay)
		defer timer.Stop()
		select {
		case err := <-done:
			return err
		case <-timer.C:
			return fmt.Errorf("wait for cancelled command: %w", ctx.Err())
		}
	}
}

func commandContextError(displayName string, result Result, ctxErr error) error {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		if d := result.Duration.Round(time.Millisecond); d > 0 {
			return fmt.Errorf(commandRunTimeoutAfterFormat, displayName, d, ctxErr)
		}
		return fmt.Errorf(commandRunTimeoutFormat, displayName, ctxErr)
	}
	return fmt.Errorf(commandRunErrorFormat, displayName, ctxErr)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, _ = b.buf.Write(p) // bytes.Buffer writes all bytes and never returns an error.
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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
	//nolint:wrapcheck // CommandRunner already attaches the canonical command context; preserve injected runner errors for callers.
	return r.Run(ctx, name, args...)
}

// RunUser is the fortified equivalent of Run for a command that must execute as
// a specific OS user. If the runner cannot change users, it fails closed.
func RunUser(ctx context.Context, r Runner, timeout time.Duration, user, name string, args ...string) (Result, error) {
	ctx, cancel := deadline(ctx, timeout)
	defer cancel()

	if ur, ok := r.(UserRunner); ok {
		//nolint:wrapcheck // CommandRunner already attaches the canonical command context; preserve injected runner errors for callers.
		return ur.RunUser(ctx, user, name, args...)
	}
	return Result{ExitCode: ExitCodeRunFailure}, fmt.Errorf("execx: runner does not support user %q", user)
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
		//nolint:wrapcheck // CommandRunner already attaches the canonical command context; preserve injected runner errors for callers.
		return er.RunEnv(ctx, env, name, args...)
	}
	return Result{}, fmt.Errorf("execx: runner does not support custom environment (got %T)", r)
}

// IsContextErr reports whether err is a context cancellation or deadline.
func IsContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// ContextFailure formats a context deadline or cancel error for operator-facing
// check messages when the timeout is enforced by context.WithTimeout rather than
// execx.Run directly.
func ContextFailure(err error, timeout time.Duration) string {
	return OperatorFailure(err, Result{ExitCode: ExitCodeRunFailure}, timeout)
}

// ContextError wraps ContextFailure as an error, preserving a nil err. For
// callers that propagate the operator-facing message as an error value.
func ContextError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(ContextFailure(err, NoTimeout))
}

// FormatContextOrError returns an operator-facing message for context errors, or
// err.Error() for other failures.
func FormatContextOrError(err error, timeout time.Duration) string {
	if IsContextErr(err) {
		return ContextFailure(err, timeout)
	}
	return err.Error()
}

// OperatorFailure formats a command run failure for check, probe and hook status
// messages. ExitCodeRunFailure from execx marks a run failure (timeout, missing
// binary, ...), not a real process exit status. Timeouts are reported as
// "timeout after <duration>" instead of "exit -1" or "context deadline exceeded".
func OperatorFailure(err error, res Result, timeout time.Duration) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		d := timeout
		if d <= 0 && res.Duration > 0 {
			d = res.Duration.Round(time.Millisecond)
		}
		if d > 0 {
			return fmt.Sprintf("timeout after %s", d)
		}
		return "timeout"
	}
	msg := err.Error()
	if after, ok := strings.CutPrefix(msg, commandRunErrorPrefix); ok {
		if _, detail, ok := strings.Cut(after, commandRunErrorSeparator); ok {
			return detail
		}
	}
	return msg
}
