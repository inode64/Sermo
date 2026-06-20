package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
)

// HookSpec is a watch's hook action: a local command (argv, never a shell) run
// with a timeout when the watch condition fires. Beyond
// running the command it can assert the outcome: the exit statuses (ExpectExit,
// default 0) and the captured stdout/stderr (Stdout/Stderr matchers). A failed
// assertion turns the hook into a "hook-failed" event, the same as a command
// that could not run.
type HookSpec struct {
	Command    []string
	Timeout    time.Duration
	ExpectExit []int // nil means "expect 0"
	Stdout     checks.OutputMatcher
	Stderr     checks.OutputMatcher
}

// HookRunner executes a hook command with environment and a timeout, returning
// the captured result. The error is non-nil only when the command could not be
// run (failed to start, or killed by the timeout); a non-zero exit is reported
// through Result.ExitCode for the caller to evaluate against ExpectExit.
type HookRunner interface {
	RunHook(ctx context.Context, argv []string, env map[string]string, timeout time.Duration) (execx.Result, error)
}

// HookRunnerFunc adapts a plain error-returning function to HookRunner; the
// returned Result is the zero value (a clean exit 0), so existing test stubs that
// signal failure by returning a non-nil error keep working unchanged.
type HookRunnerFunc func(ctx context.Context, argv []string, env map[string]string, timeout time.Duration) error

// RunHook calls the adapted function, mapping its error to a (zero Result, err).
func (f HookRunnerFunc) RunHook(ctx context.Context, argv []string, env map[string]string, timeout time.Duration) (execx.Result, error) {
	return execx.Result{}, f(ctx, argv, env, timeout)
}

// Run executes the hook and validates its outcome: the command must run, exit
// with ExpectExit (default 0), and satisfy the stdout/stderr matchers. It returns
// a descriptive error on the first failed expectation, or nil when all pass.
func (h HookSpec) Run(ctx context.Context, runner HookRunner, env map[string]string) error {
	if len(h.Command) == 0 {
		return errors.New("hook has no command")
	}
	res, err := runner.RunHook(ctx, h.Command, env, h.Timeout)
	if err != nil {
		return err
	}
	if !checks.ExitCodeExpected(res.ExitCode, h.ExpectExit) {
		detail := fmt.Sprintf("exit %d (want %s)", res.ExitCode, checks.ExpectExitText(h.ExpectExit))
		if s := checks.FirstNonEmptyLine(res.Stderr); s != "" {
			detail += ": " + s
		}
		return errors.New(detail)
	}
	if ok, detail := h.Stdout.Match(res.Stdout); !ok {
		return fmt.Errorf("stdout %s", detail)
	}
	if ok, detail := h.Stderr.Match(res.Stderr); !ok {
		return fmt.Errorf("stderr %s", detail)
	}
	return nil
}

// OSHookRunner runs hooks using execx (argv only, no shell). It merges the
// current process environment with the SERMO_* variables provided by the
// caller and respects the per-hook timeout.
type OSHookRunner struct {
	// Runner is the execx runner to use. If nil, CommandRunner is used.
	Runner execx.Runner
}

// RunHook builds the environment (os.Environ + injected vars) and executes the
// command through execx, applying the timeout when positive. A non-zero exit is
// returned in the Result with a nil error (it is an expected outcome to assert);
// only a genuine run failure (could not start, or timeout, marked by a negative
// ExitCode) is returned as an error.
func (r OSHookRunner) RunHook(ctx context.Context, argv []string, env map[string]string, timeout time.Duration) (execx.Result, error) {
	if len(argv) == 0 {
		return execx.Result{}, errors.New("hook command is empty")
	}

	fullEnv := os.Environ()
	for k, v := range env {
		fullEnv = append(fullEnv, k+"="+v)
	}

	runner := r.Runner
	if runner == nil {
		runner = execx.CommandRunner{}
	}

	// RunEnv honors the custom environment and applies the timeout (if > 0).
	res, err := execx.RunEnv(ctx, runner, fullEnv, timeout, argv[0], argv[1:]...)
	if err != nil && res.ExitCode < 0 {
		return res, err
	}
	return res, nil
}

// defaultHookRunner returns the provided runner, or a default OSHookRunner
// (which delegates to execx.CommandRunner and therefore goes through the
// project's sanctioned exec path with context + timeout) if nil.
//
// This centralizes the defensive fallback used by watches when no runner
// is injected (common in unit tests that construct Watch / fileWatcher /
// procWatcher directly).
func defaultHookRunner(r HookRunner) HookRunner {
	if r != nil {
		return r
	}
	return OSHookRunner{}
}
