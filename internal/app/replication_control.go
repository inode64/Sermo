package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sermo/internal/checks"
	"sermo/internal/locks"
)

const replicationControlLockPrefix = "replication-"

// ControlReplicationStart serializes a manual replication start for one watch,
// bounds it by timeout and delegates the live status preflight plus the
// post-start verification to the checks package. It mirrors ControlRAID: an
// explicitly requested operator action, never autonomous remediation.
func ControlReplicationStart(ctx context.Context, runtimeDir, watch string, entry map[string]any, timeout time.Duration) checks.ReplicationControlResult {
	if timeout <= 0 {
		timeout = DefaultEngineOperationTimeout
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	locker := configureOperationLocker(runtimeDir, nil)
	handle, err := locker.Acquire(replicationControlLockPrefix+watch, timeout)
	if err != nil {
		if _, held := errors.AsType[*locks.HeldError](err); held {
			return checks.ReplicationControlResult{Message: fmt.Sprintf("replication watch %q already has an operation in progress", watch)}
		}
		return checks.ReplicationControlResult{Message: fmt.Sprintf("lock replication watch %q: %v", watch, err)}
	}
	defer func() { _ = handle.Release() }()

	return checks.StartReplication(opCtx, entry)
}
