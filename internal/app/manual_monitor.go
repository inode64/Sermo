package app

import (
	"fmt"

	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/state"
)

// ManualMonitorChange describes an automatic monitoring-state adjustment caused
// by a successful manual service start or stop.
type ManualMonitorChange struct {
	Changed   bool
	Monitored bool
	Action    string
	Message   string
}

// SyncManualActionMonitoringWithActive pauses monitoring after a successful
// manual stop and restores it after a successful manual start when the stop
// created the pause. Existing manual unmonitor state is preserved.
// activeAfterStart restores monitoring for starts that reached the backend but
// failed postflight when the service is still active.
func SyncManualActionMonitoringWithActive(store MonitorStore, service, action string, result operation.Result, stopSource, restoreSource string, activeAfterStart bool) (ManualMonitorChange, error) {
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
		if !result.OK() && !activeAfterStart {
			return ManualMonitorChange{}, nil
		}
		return syncMonitorRestore(store, service, service, restoreSource,
			eventMessageMonitoringResumedAfterManualStart, state.IsManualStopSource)
	default:
		return ManualMonitorChange{}, nil
	}
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
