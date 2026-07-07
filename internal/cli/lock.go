package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"sermo/internal/config"
	"sermo/internal/locks"
)

// runLock dispatches the named-lock commands ():
//
//	lock SERVICE [--name N] --reason R --ttl D -- COMMAND...   (hold for COMMAND)
//	lock acquire SERVICE [--name N] --reason R --ttl D         (persistent)
//	lock release SERVICE [--name N]
func (a App) runLock(ctx context.Context, opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError(commandLock, "lock requires a service or subcommand")
	}

	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}
	locker := locks.NewNamedLocker(filepath.Join(cfg.Global.RuntimeDir(), "locks"))

	switch opts.args[0] {
	case "acquire":
		return a.runLockAcquire(opts, cfg, locker, opts.args[1:])
	case "release":
		return a.runLockRelease(opts, cfg, locker, opts.args[1:])
	default:
		return a.runLockWrap(ctx, opts, cfg, locker, opts.args[0])
	}
}

func (a App) runLockAcquire(opts options, cfg *config.Config, locker locks.NamedLocker, args []string) int {
	if len(args) == 0 {
		return a.commandUsageError(commandLock, "lock acquire requires a service name")
	}
	if len(args) > 1 {
		return a.commandUsageError(commandLock, "lock acquire takes exactly one service name")
	}
	if code := requireLockMeta(a, opts); code != exitSuccess {
		return code
	}
	service := canonicalServiceIfKnown(cfg, args[0])

	path, err := locker.Pin(service, opts.name, opts.reason, opts.ttl)
	if err != nil {
		a.recordAccess(cfg, accessCommandLockAcquire, service, accessStatusError, err.Error())
		return a.reportLockError(opts, err)
	}
	a.recordAccess(cfg, accessCommandLockAcquire, service, accessStatusOK, path)
	fmt.Fprintf(a.Stdout, "acquired %s\n", path)
	return exitSuccess
}

func (a App) runLockRelease(opts options, cfg *config.Config, locker locks.NamedLocker, args []string) int {
	if len(args) == 0 {
		return a.commandUsageError(commandLock, "lock release requires a service name")
	}
	if len(args) > 1 {
		return a.commandUsageError(commandLock, "lock release takes exactly one service name")
	}
	service := canonicalServiceIfKnown(cfg, args[0])
	if err := locker.Release(service, opts.name); err != nil {
		a.recordAccess(cfg, accessCommandLockRelease, service, accessStatusError, err.Error())
		return a.fail(opts, fmt.Sprintf("release failed: %v", err))
	}
	a.recordAccess(cfg, accessCommandLockRelease, service, accessStatusOK, lockID(service, opts.name))
	fmt.Fprintf(a.Stdout, "released %s\n", lockID(service, opts.name))
	return exitSuccess
}

func (a App) runLockWrap(ctx context.Context, opts options, cfg *config.Config, locker locks.NamedLocker, service string) int {
	if len(opts.args) > 1 {
		return a.commandUsageError(commandLock, "lock wrap takes exactly one service name before --")
	}
	if len(opts.commandArgs) == 0 {
		return a.commandUsageError(commandLock, "lock SERVICE ... -- COMMAND requires a command after --")
	}
	if code := requireLockMeta(a, opts); code != exitSuccess {
		return code
	}
	service = canonicalServiceIfKnown(cfg, service)

	handle, err := locker.Hold(service, opts.name, opts.reason, opts.ttl)
	if err != nil {
		a.recordAccess(cfg, accessCommandLockWrap, service, accessStatusError, err.Error())
		return a.reportLockError(opts, err)
	}
	defer func() { _ = handle.Release() }()

	cmd := exec.CommandContext(ctx, opts.commandArgs[0], opts.commandArgs[1:]...) //nolint:gosec // G204: runs the operator-provided locked command, by design
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			a.recordAccess(cfg, accessCommandLockWrap, service, accessStatusError, fmt.Sprintf("exit %d", exitErr.ExitCode()))
			return exitErr.ExitCode()
		}
		a.recordAccess(cfg, accessCommandLockWrap, service, accessStatusError, err.Error())
		return a.fail(opts, fmt.Sprintf("run command: %v", err))
	}
	a.recordAccess(cfg, accessCommandLockWrap, service, accessStatusOK, opts.commandArgs[0])
	return exitSuccess
}

func requireLockMeta(a App, opts options) int {
	if opts.reason == "" {
		return a.commandUsageError(commandLock, "--reason is required")
	}
	if opts.ttl <= 0 {
		return a.commandUsageError(commandLock, "--ttl is required and must be positive")
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
	return a.fail(opts, fmt.Sprintf("lock failed: %v", err))
}

func lockID(service, name string) string {
	if name != "" {
		return service + "." + name
	}
	return service
}
