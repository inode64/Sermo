package app

import "strings"

const (
	TargetStateDisabled    = "disabled"
	TargetStateRunning     = "running"
	TargetStateStopped     = "stopped"
	TargetStateMonitorized = "monitorized"
	TargetStateFailed      = "failed"
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
// same operator-facing state vocabulary used for services.
func WatchState(enabled, monitored, failed bool) string {
	if !enabled {
		return TargetStateDisabled
	}
	if !monitored {
		return TargetStateStopped
	}
	if failed {
		return TargetStateFailed
	}
	return TargetStateMonitorized
}

// WatchActivityFailed reports whether an event kind represents a failed watch
// side-effect. The dashboard uses it as the current best-effort watch health
// signal because watches do not publish service-style check snapshots.
func WatchActivityFailed(kind string) bool {
	return strings.HasSuffix(kind, "-failed")
}
