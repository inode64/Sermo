package app

import (
	"fmt"

	"sermo/internal/locks"
	"sermo/internal/severity"
)

// configureOperationLocker returns the per-service internal operation locker with
// stale-lock reclaims logged through onReclaim (AGENTS.md).
func configureOperationLocker(runtimeDir string, onReclaim func(service, reason string)) locks.OperationLocker {
	locker := locks.NewOperationLocker(locks.RuntimeOpsDir(runtimeDir))
	if onReclaim != nil {
		locker.OnReclaim = onReclaim
	}
	return locker
}

// operationLockReclaimEvent adapts the daemon event log to OperationLocker.OnReclaim.
func operationLockReclaimEvent(emit func(Event)) func(service, reason string) {
	if emit == nil {
		return nil
	}
	return func(service, reason string) {
		emit(Event{
			Service: service,
			Kind:    eventKindAlert,
			// The lock's holder died mid-operation: worth a look, not an outage.
			Severity: severity.Warning,
			Message:  fmt.Sprintf("reclaimed stale operation lock (%s)", reason),
			Notice:   true,
		})
	}
}
