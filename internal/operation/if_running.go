package operation

import (
	"context"
	"errors"
	"fmt"

	"sermo/internal/config"
	"sermo/internal/servicemgr"
)

// checkRunning runs under the operation lock, before any potentially mutating
// preflight hook or reconciliation. An inactive target must never reach start.
func (e Engine) checkRunning(ctx context.Context, action string, result *Result) bool {
	if action != actionStop && action != actionRestart && action != actionReload {
		result.Status, result.Message = ResultFailed, "if-running is only available for stop, restart and reload"
		return false
	}
	if e.Manager == nil {
		result.Status, result.Message = ResultFailed, "if-running requires a service manager"
		return false
	}
	status, err := e.Manager.Status(ctx, e.Unit)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if errors.Is(err, servicemgr.ErrNotFound) {
		return skipNotRunning(result)
	}
	if err != nil {
		return failPhase(ctx, result, "operation timed out querying running state", "query running state: ", err)
	}
	switch status.Status {
	case servicemgr.StatusInactive, servicemgr.StatusFailed, servicemgr.StatusPaused:
		return skipNotRunning(result)
	case servicemgr.StatusActive:
		if e.Lifecycle.ProcessMode != config.ServiceProcessResident {
			return true
		}
	default:
		result.Status, result.Message = ResultFailed, fmt.Sprintf("cannot determine running state: %s", status.Status)
		return false
	}
	observation, err := e.observeProcesses(ctx)
	if err != nil {
		return failPhase(ctx, result, "operation timed out observing running service", "observe running service: ", err)
	}
	principal := observation.Principal()
	if principal.Attributed {
		if err := e.identityRefusal(principal); err != nil {
			result.Status, result.Message = ResultBlocked, err.Error()
			return false
		}
		return true
	}
	if observation.AbsenceKnown {
		return skipNotRunning(result)
	}
	result.Status, result.Message = ResultFailed, "cannot determine whether the service process is running"
	return false
}

func skipNotRunning(result *Result) bool {
	result.Status, result.Message = ResultSkipped, "service is not running; action skipped"
	return false
}
