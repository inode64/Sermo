package app

import (
	"context"
	"errors"
	"os"
	"time"

	"sermo/internal/execx"
)

// HookSpec is a watch's hook action: a local command (argv, never a shell) run
// with a timeout when the watch condition fires (section 16, extension).
type HookSpec struct {
	Command []string
	Timeout time.Duration
}

// HookRunner executes a hook command with environment and a timeout.
type HookRunner interface {
	RunHook(ctx context.Context, argv []string, env map[string]string, timeout time.Duration) error
}

// HookRunnerFunc adapts a function to HookRunner.
type HookRunnerFunc func(ctx context.Context, argv []string, env map[string]string, timeout time.Duration) error

// RunHook calls the adapted function.
func (f HookRunnerFunc) RunHook(ctx context.Context, argv []string, env map[string]string, timeout time.Duration) error {
	return f(ctx, argv, env, timeout)
}

// Run validates the spec and dispatches it through the runner.
func (h HookSpec) Run(ctx context.Context, runner HookRunner, env map[string]string) error {
	if len(h.Command) == 0 {
		return errors.New("hook has no command")
	}
	return runner.RunHook(ctx, h.Command, env, h.Timeout)
}

// OSHookRunner runs hooks using execx (argv only, no shell). It merges the
// current process environment with the SERMO_* variables provided by the
// caller and respects the per-hook timeout.
type OSHookRunner struct {
	// Runner is the execx runner to use. If nil, CommandRunner is used.
	Runner execx.Runner
}

// RunHook builds the environment (os.Environ + injected vars) and executes
// the command through execx, applying the timeout when positive.
func (r OSHookRunner) RunHook(ctx context.Context, argv []string, env map[string]string, timeout time.Duration) error {
	if len(argv) == 0 {
		return errors.New("hook command is empty")
	}

	fullEnv := os.Environ()
	for k, v := range env {
		fullEnv = append(fullEnv, k+"="+v)
	}

	runner := r.Runner
	if runner == nil {
		runner = execx.CommandRunner{}
	}

	// We use RunEnv so that the custom environment is honored.
	// The helper also applies the timeout (if > 0).
	_, err := execx.RunEnv(ctx, runner, fullEnv, timeout, argv[0], argv[1:]...)
	return err
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
