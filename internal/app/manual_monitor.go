package app

import (
	"fmt"

	"sermo/internal/operation"
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

// SyncManualActionMonitoring pauses monitoring after a successful manual stop
// and restores it after a successful manual start when the stop created the
// pause. Existing manual unmonitor state is preserved.
func SyncManualActionMonitoring(store MonitorStore, service, action string, result operation.Result, stopSource, restoreSource string) (ManualMonitorChange, error) {
	if store == nil || !result.OK() {
		return ManualMonitorChange{}, nil
	}
	switch action {
	case "stop":
		active, found, err := store.Active(service)
		if err != nil {
			return ManualMonitorChange{}, fmt.Errorf("read monitoring state for %s: %w", service, err)
		}
		if found && !active {
			return ManualMonitorChange{}, nil
		}
		if err := store.SetActive(service, false, stopSource); err != nil {
			return ManualMonitorChange{}, fmt.Errorf("pause monitoring for %s: %w", service, err)
		}
		return ManualMonitorChange{
			Changed:   true,
			Monitored: false,
			Action:    "unmonitor",
			Message:   "monitoring paused after manual stop",
		}, nil
	case "start":
		rec, found, err := store.MonitorState(service)
		if err != nil {
			return ManualMonitorChange{}, fmt.Errorf("read monitoring state for %s: %w", service, err)
		}
		if !found || rec.Active || !state.IsManualStopSource(rec.Source) {
			return ManualMonitorChange{}, nil
		}
		if err := store.SetActive(service, true, restoreSource); err != nil {
			return ManualMonitorChange{}, fmt.Errorf("resume monitoring for %s: %w", service, err)
		}
		return ManualMonitorChange{
			Changed:   true,
			Monitored: true,
			Action:    "monitor",
			Message:   "monitoring resumed after manual start",
		}, nil
	default:
		return ManualMonitorChange{}, nil
	}
}
