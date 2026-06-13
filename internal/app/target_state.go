package app

import "strings"

// Operator-facing target states a service or watch can be reported in, as shown
// by sermoctl and the web dashboard.
const (
	TargetStateDisabled      = "disabled"
	TargetStateRunning       = "running"
	TargetStateStopped       = "stopped"
	TargetStateOK            = "ok"
	TargetStateMonitorized   = "monitorized"
	TargetStateUnmonitorized = "unmonitorized"
	TargetStateFailed        = "failed"
)

// ServiceState folds config, backend status and monitoring health into the
// single operator-facing state shown by sermoctl and the web dashboard.
func ServiceState(enabled, monitored bool, backendStatus, checkHealth string) string {
	if !enabled {
		return TargetStateDisabled
	}
	active := strings.EqualFold(backendStatus, "active")
	if !monitored {
		if active {
			return TargetStateRunning
		}
		return TargetStateStopped
	}
	if !active || checkHealth == "failing" {
		return TargetStateFailed
	}
	return TargetStateMonitorized
}

// WatchState folds config, monitor state and the last known watch error into the
// operator-facing state shown for host watches. Watches are not service-manager
// units, so they do not have running/stopped states.
func WatchState(enabled, monitored, failed bool) string {
	if !enabled {
		return TargetStateDisabled
	}
	if !monitored {
		return TargetStateUnmonitorized
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
