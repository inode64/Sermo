package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
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

// OSHookRunner runs hooks via os/exec: argv only (no shell), the daemon's
// environment plus the provided SERMO_* variables, bounded by timeout.
type OSHookRunner struct{}

// RunHook executes argv via os/exec (no shell) with the given environment,
// bounded by timeout when positive.
func (OSHookRunner) RunHook(ctx context.Context, argv []string, env map[string]string, timeout time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // G204: runs the operator-configured hook command, by design
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd.Run()
}
