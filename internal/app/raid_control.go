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

// ControlRAID serializes a pause/resume request for one md array, bounds it by
// timeout and delegates the live preflight plus sysfs post-verification to the
// checks package. It is shared by the CLI and Web backend.
func ControlRAID(ctx context.Context, runtimeDir, array, action string, timeout time.Duration) RAIDControlResult {
	if action != RaidControlPause && action != RaidControlResume {
		return RAIDControlResult{Message: fmt.Sprintf("unsupported RAID action %q", action)}
	}
	if timeout <= 0 {
		timeout = DefaultEngineOperationTimeout
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	locker := configureOperationLocker(runtimeDir, nil)
	handle, err := locker.Acquire(raidControlLockPrefix+array, timeout)
	if err != nil {
		var held *locks.HeldError
		if errors.As(err, &held) {
			return RAIDControlResult{Message: fmt.Sprintf("RAID array %q already has an operation in progress", array)}
		}
		return RAIDControlResult{Message: fmt.Sprintf("lock RAID array %q: %v", array, err)}
	}
	defer func() { _ = handle.Release() }()

	resume := action == RaidControlResume
	if _, err := checks.SetRaidRebuildState(opCtx, array, resume); err != nil {
		return RAIDControlResult{Message: err.Error()}
	}
	verb := "paused"
	if resume {
		verb = "resumed"
	}
	return RAIDControlResult{OK: true, Message: fmt.Sprintf("RAID reconstruction for %s %s", array, verb)}
}
