package app

import (
	"errors"
	"fmt"

	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/state"
)

// ManualOperationSources identifies the caller that owns a manual operation.
type ManualOperationSources struct {
	Stop, Restore, Settling string
}

// ManualMonitorChange describes an automatic monitoring-state adjustment caused
// by a successful manual service start or stop.
type ManualMonitorChange struct {
	Changed   bool
	Monitored bool
	Action    string
	Message   string
}

// SyncManualActionMonitoring pauses monitoring after a successful
// manual stop and restores it after a successful manual start when the stop
// created the pause. Existing manual unmonitor state is preserved.
// activeAfterPostflightFailure restores monitoring for starts that reached the backend but
// failed postflight when the service is still active.
func SyncManualActionMonitoring(store MonitorStore, service, action string, result operation.Result, stopSource, restoreSource string, activeAfterPostflightFailure bool) (ManualMonitorChange, error) {
	if store == nil {
		return ManualMonitorChange{}, nil
	}
	switch rules.ActionType(action) {
	case rules.ActionStop:
		if !result.OK() {
			return ManualMonitorChange{}, nil
		}
		return syncMonitorPause(store, service, service, stopSource, eventMessageMonitoringPausedAfterManualStop)
	case rules.ActionStart, rules.ActionRestart, rules.ActionResume:
		if !result.OK() && !activeAfterPostflightFailure {
			return ManualMonitorChange{}, nil
		}
		return syncMonitorRestore(store, service, service, restoreSource,
			eventMessageMonitoringResumedAfterManualStart, state.IsManualStopSource)
	default:
		return ManualMonitorChange{}, nil
	}
}

// CompleteManualOperation applies the common post-operation state transition.
// It always finalizes observation settling even when monitoring state storage
// fails, so a storage error cannot leave alerts suppressed indefinitely.
func CompleteManualOperation(monitor MonitorStore, settling OperationSettlingStore, service, action string, result operation.Result, opErr error, sources ManualOperationSources, activeAfterPostflightFailure bool) (ManualMonitorChange, error) {
	var change ManualMonitorChange
	var monitorErr error
	if opErr == nil {
		change, monitorErr = SyncManualActionMonitoring(monitor, service, action, result, sources.Stop, sources.Restore, activeAfterPostflightFailure)
	}
	settlingErr := finishOperationSettling(settling, service, action, sources.Settling, result, opErr, activeAfterPostflightFailure)
	return change, errors.Join(monitorErr, settlingErr)
}

// syncMonitorPause records a monitoring pause for key unless the subject is
// already manually unmonitored; the pause half of the state machine shared by
// the manual-action and storage-mount monitors.
func syncMonitorPause(store MonitorStore, key, subject, source, message string) (ManualMonitorChange, error) {
	active, found, err := store.Active(key)
	if err != nil {
		return ManualMonitorChange{}, fmt.Errorf("read monitoring state for %s: %w", subject, err)
	}
	if found && !active {
		return ManualMonitorChange{}, nil
	}
	if err := store.SetActive(key, false, source); err != nil {
		return ManualMonitorChange{}, fmt.Errorf("pause monitoring for %s: %w", subject, err)
	}
	return ManualMonitorChange{
		Changed:   true,
		Monitored: false,
		Action:    eventActionUnmonitor,
		Message:   message,
	}, nil
}

// syncMonitorRestore lifts a monitoring pause for key when the recorded pause
// came from a source accepted by sourceOK; the restore half of the shared
// state machine. Pauses from other sources (e.g. a manual unmonitor) survive.
func syncMonitorRestore(store MonitorStore, key, subject, source, message string, sourceOK func(string) bool) (ManualMonitorChange, error) {
	rec, found, err := store.MonitorState(key)
	if err != nil {
		return ManualMonitorChange{}, fmt.Errorf("read monitoring state for %s: %w", subject, err)
	}
	if !found || rec.Active || !sourceOK(rec.Source) {
		return ManualMonitorChange{}, nil
	}
	if err := store.SetActive(key, true, source); err != nil {
		return ManualMonitorChange{}, fmt.Errorf("resume monitoring for %s: %w", subject, err)
	}
	return ManualMonitorChange{
		Changed:   true,
		Monitored: true,
		Action:    eventActionMonitor,
		Message:   message,
	}, nil
}
