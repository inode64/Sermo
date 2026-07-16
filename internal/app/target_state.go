package app

import (
	"strings"

	"sermo/internal/servicemgr"
)

// Operator-facing target states and monitor-filter values shown by sermoctl and
// the web dashboard.
const (
	TargetStateDisabled   = "disabled"
	TargetStateActive     = "active"
	TargetStateStarted    = "started"
	TargetStatePaused     = "paused"
	TargetStateStopped    = "stopped"
	TargetStateStarting   = "starting"
	TargetStateCollecting = "collecting"
	TargetStateOK         = "ok"
	TargetStateMonitored  = "monitored"
	TargetStateFailed     = "failed"
	TargetStateWarning    = "warning"
	TargetStateStale      = "stale"
)

const (
	checkHealthFailing = "failing"
	checkHealthUnknown = "unknown"
)

// ServiceState folds config, backend status and monitoring health into the
// operator-facing activity state shown by sermoctl and the web dashboard. The
// state is intentionally a single service-axis value: "active" means a trusted
// service process is currently confirmed, while "monitored" additionally means
// the current daemon generation has every indicator needed to show it observed.
func ServiceState(enabled, monitored bool, backendStatus, checkHealth string, observed, observabilityReady, processActive bool) string {
	if !enabled {
		return TargetStateDisabled
	}
	if monitored && !observed {
		if processActive && strings.EqualFold(backendStatus, string(servicemgr.StatusActive)) {
			return TargetStateActive
		}
		return TargetStateStarting
	}
	active := strings.EqualFold(backendStatus, string(servicemgr.StatusActive))
	failed := strings.EqualFold(backendStatus, string(servicemgr.StatusFailed))
	if failed {
		if !monitored {
			return TargetStateStopped
		}
		return TargetStateFailed
	}
	if !active {
		if monitored {
			return TargetStateFailed
		}
		return TargetStateStopped
	}
	if !monitored {
		return TargetStateStarted
	}
	switch checkHealth {
	case checkHealthFailing:
		return TargetStateFailed
	case checkHealthUnknown:
		if processActive {
			return TargetStateActive
		}
		return TargetStateCollecting
	}
	if !observabilityReady {
		if processActive {
			return TargetStateActive
		}
		return TargetStateCollecting
	}
	return TargetStateMonitored
}

// WatchState folds config, monitor state and the last known watch error into the
// operator-facing health state shown for host watches. Watches are not
// service-manager units, so a paused watch is disabled from the active checking
// set rather than started/stopped.
func WatchState(enabled, monitored, failed, observed bool) string {
	if !enabled || !monitored {
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
	if kind == eventKindFiring {
		return true
	}
	return strings.HasSuffix(kind, eventKindFailedSuffix)
}
