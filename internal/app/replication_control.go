package app

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/checks"
)

const replicationControlLockPrefix = "replication-"

// ControlReplicationStart serializes a manual replication start for one watch,
// bounds it by timeout and delegates the live status preflight plus the
// post-start verification to the checks package. It mirrors ControlRAID: an
// explicitly requested operator action, never autonomous remediation.
// onReclaim audits a reclaimed stale lock.
func ControlReplicationStart(ctx context.Context, runtimeDir, watch string, entry map[string]any, timeout time.Duration, onReclaim func(reason string)) checks.ReplicationControlResult {
	ok, message := runLockedControl(ctx, runtimeDir, replicationControlLockPrefix+watch, fmt.Sprintf("replication watch %q", watch), timeout, onReclaim, func(opCtx context.Context) (bool, string) {
		result := checks.StartReplication(opCtx, entry)
		return result.OK, result.Message
	})
	return checks.ReplicationControlResult{OK: ok, Message: message}
}
