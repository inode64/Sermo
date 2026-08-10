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
	checkHealthWarning = "warning"
)

// ServiceState folds config, backend status and monitoring health into the
// operator-facing activity state shown by sermoctl and the web dashboard. The
// state is intentionally a single service-axis value: "active" means a trusted
// service process is currently confirmed, while "monitored" additionally means
// the current daemon generation has every indicator needed to show it observed.
//
// processesMissing marks the one indicator gap that never resolves on its own:
// the unit is active and its checks pass, but the daemon attributes no process
// to it, so runtime metrics will not arrive until the service definition or
// the host changes. That is a monitoring blind spot to act on rather than a
// cycle to wait out, so it settles on "warning" instead of "collecting".
func ServiceState(enabled, monitored bool, backendStatus, checkHealth string, observed, observabilityReady, processActive, processesMissing, backendDegraded bool) string {
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
		// The init manager's failure remains visible through Status, but a live
		// exact process plus passing functional checks proves the workload is
		// still serving. It is degraded rather than an outage; operations keep
		// using the failed backend state and therefore retain their normal gates.
		if backendDegraded {
			return TargetStateWarning
		}
		if !monitored {
			return TargetStateStopped
		}
		return TargetStateFailed
	}
	// "unknown" is the backend declining to answer, not an answer of "down": an
	// init script that overrides `status` with its own report, or a query that
	// timed out, reads unknown while the service runs normally. Reporting that
	// as failed invents an outage out of a failed observation, so it is not a
	// verdict — the service's own checks below decide instead. Every other
	// inactive status *is* a verdict and still fails.
	unknown := strings.EqualFold(backendStatus, string(servicemgr.StatusUnknown))
	if !active {
		// Unmonitored keeps reading "stopped": nothing alerts on it either way,
		// and "started" would be just as unfounded.
		if !monitored {
			return TargetStateStopped
		}
		if !unknown {
			return TargetStateFailed
		}
	} else if !monitored {
		return TargetStateStarted
	}
	switch checkHealth {
	case checkHealthFailing:
		return TargetStateFailed
	case checkHealthWarning:
		return TargetStateWarning
	case checkHealthUnknown:
		if processActive {
			return TargetStateActive
		}
		return TargetStateCollecting
	}
	// Checks are healthy, but a backend that would not answer cannot underwrite
	// the full-observability claim "monitored" makes.
	if unknown {
		if processActive {
			return TargetStateActive
		}
		return TargetStateCollecting
	}
	if !observabilityReady {
		if processActive {
			return TargetStateActive
		}
		if processesMissing {
			return TargetStateWarning
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
