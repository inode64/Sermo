package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sermo/internal/checks"
	"sermo/internal/locks"
)

const (
	// RaidControlPause pauses an active md reconstruction.
	RaidControlPause = "pause"
	// RaidControlResume resumes a paused md array.
	RaidControlResume     = "resume"
	raidControlLockPrefix = "raid-"
)

// RAIDControlResult is the verified outcome of a manual RAID control action.
type RAIDControlResult struct {
	OK      bool
	Message string
}

// runLockedControl is the shared ritual of every manual control action: bound
// it by timeout, serialize it under a named operation lock (reporting a held
// lock as "already has an operation in progress"), and run the action inside
// both. subject names the thing being controlled in the two lock messages.
func runLockedControl(ctx context.Context, runtimeDir, lockName, subject string, timeout time.Duration, run func(context.Context) (bool, string)) (ok bool, message string) {
	if timeout <= 0 {
		timeout = DefaultEngineOperationTimeout
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	locker := configureOperationLocker(runtimeDir, nil)
	handle, err := locker.Acquire(lockName, timeout)
	if err != nil {
		if _, held := errors.AsType[*locks.HeldError](err); held {
			return false, subject + " already has an operation in progress"
		}
		return false, fmt.Sprintf("lock %s: %v", subject, err)
	}
	defer func() { _ = handle.Release() }()
	return run(opCtx)
}

// ControlRAID serializes a pause/resume request for one md array, bounds it by
// timeout and delegates the live preflight plus sysfs post-verification to the
// checks package. It is shared by the CLI and Web backend.
func ControlRAID(ctx context.Context, runtimeDir, array, action string, timeout time.Duration) RAIDControlResult {
	if action != RaidControlPause && action != RaidControlResume {
		return RAIDControlResult{Message: fmt.Sprintf("unsupported RAID action %q", action)}
	}
	ok, message := runLockedControl(ctx, runtimeDir, raidControlLockPrefix+array, fmt.Sprintf("RAID array %q", array), timeout, func(opCtx context.Context) (bool, string) {
		resume := action == RaidControlResume
		if _, err := checks.SetRaidRebuildState(opCtx, array, resume); err != nil {
			return false, err.Error()
		}
		verb := "paused"
		if resume {
			verb = "resumed"
		}
		return true, fmt.Sprintf("RAID reconstruction for %s %s", array, verb)
	})
	return RAIDControlResult{OK: ok, Message: message}
}
