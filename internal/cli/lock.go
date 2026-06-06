package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"sermo/internal/locks"
)

// runLock dispatches the named-lock commands (post-MVP, section 20):
//
//	lock SERVICE [--name N] --reason R --ttl D -- COMMAND...   (hold for COMMAND)
//	lock acquire SERVICE [--name N] --reason R --ttl D         (persistent)
//	lock release SERVICE [--name N]
func (a App) runLock(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		fmt.Fprintln(a.Stderr, "usage error: lock requires a service or subcommand")
		writeUsage(a.Stderr)
		return exitUsage
	}

	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}
	locker := locks.NewNamedLocker(filepath.Join(cfg.Global.RuntimeDir(), "locks"))

	switch opts.args[0] {
	case "acquire":
		return a.runLockAcquire(opts, locker, opts.args[1:])
	case "release":
		return a.runLockRelease(opts, locker, opts.args[1:])
	default:
		return a.runLockWrap(ctx, opts, locker, opts.args[0])
	}
}

func (a App) runLockAcquire(opts options, locker locks.NamedLocker, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Stderr, "usage error: lock acquire requires a service name")
		return exitUsage
	}
	if code := requireLockMeta(a, opts); code != exitSuccess {
		return code
	}
	service := args[0]

	path, err := locker.Pin(service, opts.name, opts.reason, opts.ttl)
	if err != nil {
		return a.reportLockError(opts, err)
	}
	fmt.Fprintf(a.Stdout, "acquired %s\n", path)
	return exitSuccess
}

func (a App) runLockRelease(opts options, locker locks.NamedLocker, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(a.Stderr, "usage error: lock release requires a service name")
		return exitUsage
	}
	service := args[0]
	if err := locker.Release(service, opts.name); err != nil {
		a.reportError(opts, fmt.Sprintf("release failed: %v", err))
		return exitRuntimeError
	}
	fmt.Fprintf(a.Stdout, "released %s\n", lockID(service, opts.name))
	return exitSuccess
}

func (a App) runLockWrap(ctx context.Context, opts options, locker locks.NamedLocker, service string) int {
	if len(opts.commandArgs) == 0 {
		fmt.Fprintln(a.Stderr, "usage error: lock SERVICE ... -- COMMAND requires a command after --")
		return exitUsage
	}
	if code := requireLockMeta(a, opts); code != exitSuccess {
		return code
	}

	handle, err := locker.Hold(service, opts.name, opts.reason, opts.ttl)
	if err != nil {
		return a.reportLockError(opts, err)
	}
	defer func() { _ = handle.Release() }()

	cmd := exec.CommandContext(ctx, opts.commandArgs[0], opts.commandArgs[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		a.reportError(opts, fmt.Sprintf("run command: %v", err))
		return exitRuntimeError
	}
	return exitSuccess
}

func requireLockMeta(a App, opts options) int {
	if opts.reason == "" {
		fmt.Fprintln(a.Stderr, "usage error: --reason is required")
		return exitUsage
	}
	if opts.ttl <= 0 {
		fmt.Fprintln(a.Stderr, "usage error: --ttl is required and must be positive")
		return exitUsage
	}
	return exitSuccess
}

func (a App) reportLockError(opts options, err error) int {
	var held *locks.HeldError
	if errors.As(err, &held) {
		if opts.json {
			writeJSON(a.Stdout, map[string]string{"status": "blocked", "message": "lock already held"})
		} else {
			fmt.Fprintf(a.Stdout, "BLOCKED %s lock\nreason: lock already held\n", held.Service)
		}
		return exitBlocked
	}
	a.reportError(opts, fmt.Sprintf("lock failed: %v", err))
	return exitRuntimeError
}

func lockID(service, name string) string {
	if name != "" {
		return service + "." + name
	}
	return service
}
