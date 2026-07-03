package app

import "strings"

// Operator-facing target states and monitor-filter values shown by sermoctl and
// the web dashboard.
const (
	TargetStateDisabled      = "disabled"
	TargetStateRunning       = "running"
	TargetStatePaused        = "paused"
	TargetStateStopped       = "stopped"
	TargetStateStarting      = "starting"
	TargetStateOK            = "ok"
	TargetStateMonitorized   = "monitorized"
	TargetStateUnmonitorized = "unmonitorized"
	TargetStateFailed        = "failed"
)

// ServiceState folds config, backend status and monitoring health into the
// operator-facing activity state shown by sermoctl and the web dashboard. The
// monitor flag is a separate axis; a paused monitor can still have a running,
// stopped, paused or failed backend state.
func ServiceState(enabled, monitored bool, backendStatus, checkHealth string, observed bool) string {
	if !enabled {
		return TargetStateDisabled
	}
	if monitored && !observed {
		return TargetStateStarting
	}
	active := strings.EqualFold(backendStatus, "active")
	paused := strings.EqualFold(backendStatus, "paused")
	failed := strings.EqualFold(backendStatus, "failed")
	if paused {
		return TargetStatePaused
	}
	if failed {
		return TargetStateFailed
	}
	if !active {
		if monitored {
			return TargetStateFailed
		}
		return TargetStateStopped
	}
	if monitored && checkHealth == "failing" {
		return TargetStateFailed
	}
	return TargetStateRunning
}

// WatchState folds config, monitor state and the last known watch error into the
// operator-facing health state shown for host watches. Watches are not
// service-manager units, so they do not have running/stopped states; monitored
// versus unmonitored is reported separately.
func WatchState(enabled, monitored, failed bool, observed bool) string {
	if !enabled {
		return TargetStateDisabled
	}
	if monitored && !observed {
		return TargetStateStarting
	}
	if failed {
		return TargetStateFailed
	}
	return TargetStateOK
}

// WatchActivityFailed reports whether an event kind represents a failed watch
// side-effect or an active firing condition. The dashboard uses it as the
// current best-effort watch health signal (watches do not publish the same
// check snapshots services do). "firing" is emitted for any watch (including
// bare ones without a `then`) when its `for` window is satisfied.
func WatchActivityFailed(kind string) bool {
	if kind == "firing" {
		return true
	}
	return strings.HasSuffix(kind, "-failed")
}
