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

// Runner executes external commands. Callers must pass a context with a timeout.
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
