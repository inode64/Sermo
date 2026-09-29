package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"sermo/internal/config"
	"sermo/internal/locks"
	"sermo/internal/operation"
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
	if cfg == nil {
		return code
	}
	locker := locks.NewNamedLocker(locks.RuntimeLocksDir(cfg.Global.RuntimeDir()))

	switch opts.args[0] {
	case commandLockAcquire:
		return a.runLockAcquire(opts, cfg, locker, opts.args[1:])
	case commandLockRelease:
		return a.runLockRelease(opts, cfg, locker, opts.args[1:])
	default:
		return a.runLockWrap(ctx, opts, cfg, locker, opts.args[0])
	}
}

func (a App) runLockAcquire(opts options, cfg *config.Config, locker locks.NamedLocker, args []string) int {
	if code := a.requireSingleServiceName(len(args) > 0, len(args), commandLock, "lock acquire"); code != exitSuccess {
		return code
	}
	if code := requireLockMeta(a, opts); code != exitSuccess {
		return code
	}
	// A lock only protects a configured service: the engine checks locks by the
	// canonical service name, so a typo would protect nothing while the
	// operator believes it does.
	service, code := a.canonicalService(opts, cfg, args[0])
	if code != exitSuccess {
		return code
	}

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
	if code := a.requireSingleServiceName(len(args) > 0, len(args), commandLock, "lock release"); code != exitSuccess {
		return code
	}
	// Release still accepts a service that is no longer configured so its
	// leftover lock can be cleaned up.
	service := canonicalServiceIfKnown(cfg, args[0])
	id := locks.LockID(service, opts.name)
	if !namedLockMayExist(locks.NewScanner(locker.Dir), service, opts.name) {
		// A mistyped --name must not read as "released" while the real lock
		// keeps blocking operations.
		a.recordAccess(cfg, accessCommandLockRelease, service, accessStatusError, "no such lock "+id)
		fmt.Fprintf(a.Stdout, "no named lock %s to release\n", id)
		return exitNotActive
	}
	if err := locker.Release(service, opts.name); err != nil {
		a.recordAccess(cfg, accessCommandLockRelease, service, accessStatusError, err.Error())
		return a.fail(opts, fmt.Sprintf("release failed: %v", err))
	}
	a.recordAccess(cfg, accessCommandLockRelease, service, accessStatusOK, id)
	fmt.Fprintf(a.Stdout, "released %s\n", id)
	return exitSuccess
}

// namedLockMayExist reports whether the named lock is present. A scan failure
// or an unreadable lock file for the service counts as present, so release
// still runs rather than wrongly reporting nothing to do.
func namedLockMayExist(scanner locks.Scanner, service, name string) bool {
	report, err := scanner.Scan(service)
	if err != nil || len(report.Warnings) > 0 {
		return true
	}
	for _, lock := range report.Locks {
		if lock.Name == name {
			return true
		}
	}
	return false
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
	service, code := a.canonicalService(opts, cfg, service)
	if code != exitSuccess {
		return code
	}

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
	if err := runLockedCommand(cmd); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			code := lockedCommandExitCode(exitErr)
			a.recordAccess(cfg, accessCommandLockWrap, service, accessStatusError, fmt.Sprintf("exit %d", code))
			return code
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
	if held, ok := errors.AsType[*locks.HeldError](err); ok {
		if opts.json {
			writeJSON(a.Stdout, map[string]string{cliJSONKeyStatus: string(operation.ResultBlocked), cliJSONKeyMessage: "lock already held"})
		} else {
			fmt.Fprintf(a.Stdout, "BLOCKED %s lock\nreason: lock already held\n", held.Service)
		}
		return exitBlocked
	}
	return a.fail(opts, fmt.Sprintf("lock failed: %v", err))
}

// runLocks reports the named runtime locks for a service (active, expired and
// stale), reading the runtime root from the loaded config.
func (a App) runLocks(opts options) int {
	if code := a.requireSingleServiceName(opts.service() != "", len(opts.args), commandLocks, commandLocks); code != exitSuccess {
		return code
	}

	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	service := canonicalServiceIfKnown(cfg, opts.service())

	dir := locks.RuntimeLocksDir(cfg.Global.RuntimeDir())
	report, err := locks.NewScanner(dir).Scan(service)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("scan locks failed: %v", err))
	}

	return renderServiceList(a, opts, report.Service, cliJSONKeyLocks, report.Locks,
		report.Warnings, "no named runtime locks for %s\n", formatLock)
}

func formatLock(lock locks.Lock) string {
	id := locks.LockID(lock.Service, lock.Name)
	line := fmt.Sprintf("%s %s owner_pid=%d", id, lock.State, lock.OwnerPID)
	if !lock.ExpiresAt.IsZero() {
		line += " expires_at=" + lock.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if lock.StaleReason != "" {
		line += " (" + lock.StaleReason + ")"
	}
	if lock.Reason != "" {
		line += fmt.Sprintf(" reason=%q", lock.Reason)
	}
	return line
}

// runLockedCommand runs cmd while the caller holds its named lock. The lock
// owner is this sermoctl process, so sermoctl must outlive COMMAND: dying on a
// signal would skip the deferred release and leave a "dead owner" lock that no
// longer blocks operations while COMMAND (for example a backup cleaning up
// after SIGTERM) is still running. Termination signals are therefore caught
// for the command's lifetime. SIGTERM and SIGHUP are passed on to COMMAND;
// SIGINT and SIGQUIT are held but not forwarded: typed at a terminal they
// already reach COMMAND through the foreground process group, and a second copy
// could turn a graceful interrupt into a forced abort in tools that escalate on
// repeat.
func runLockedCommand(cmd *exec.Cmd) error {
	forwarded := []os.Signal{syscall.SIGTERM, syscall.SIGHUP}
	held := []os.Signal{syscall.SIGINT, syscall.SIGQUIT}
	sigs := make(chan os.Signal, len(forwarded)+len(held))
	signal.Notify(sigs, slices.Concat(forwarded, held)...)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case err := <-done:
			return err
		case sig := <-sigs:
			if slices.Contains(forwarded, sig) {
				// Best effort: the command may have exited already.
				_ = cmd.Process.Signal(sig)
			}
		}
	}
}

// shellSignalExitBase is the shell convention for a child killed by a signal:
// exit status 128 + signal number.
const shellSignalExitBase = 128

// lockedCommandExitCode maps COMMAND's wait status to the wrapper's exit code.
// ExitCode() is -1 for a signal death, which os.Exit turns into 255; report
// 128+signal like a shell so callers can tell a SIGTERM (143) from a failure.
func lockedCommandExitCode(exitErr *exec.ExitError) int {
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return shellSignalExitBase + int(status.Signal())
	}
	return exitErr.ExitCode()
}
