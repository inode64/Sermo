package app

import (
	"fmt"

	"sermo/internal/config"
	"sermo/internal/mountctl"
	"sermo/internal/state"
)

// SyncStorageMountMonitoring pauses a storage capacity watch after a successful
// umount and restores it after a later successful mount when the umount created
// the pause. Existing manual unmonitor state is preserved.
func SyncStorageMountMonitoring(store MonitorStore, storage, action string, resultOK bool, monitorMode string, disabled bool, pauseSource, restoreSource string) (ManualMonitorChange, error) {
	if store == nil || disabled || monitorMode == config.MonitorDisabled || !resultOK {
		return ManualMonitorChange{}, nil
	}
	key := watchMonitorKey(storage)
	switch action {
	case mountctl.ActionUmount:
		active, found, err := store.Active(key)
		if err != nil {
			return ManualMonitorChange{}, fmt.Errorf("read monitoring state for watch %s: %w", storage, err)
		}
		if found && !active {
			return ManualMonitorChange{}, nil
		}
		if err := store.SetActive(key, false, pauseSource); err != nil {
			return ManualMonitorChange{}, fmt.Errorf("pause monitoring for watch %s: %w", storage, err)
		}
		return ManualMonitorChange{
			Changed:   true,
			Monitored: false,
			Action:    "unmonitor",
			Message:   "monitoring paused after storage umount",
		}, nil
	case mountctl.ActionMount:
		rec, found, err := store.MonitorState(key)
		if err != nil {
			return ManualMonitorChange{}, fmt.Errorf("read monitoring state for watch %s: %w", storage, err)
		}
		if !found || rec.Active || !state.IsMountUmountSource(rec.Source) {
			return ManualMonitorChange{}, nil
		}
		if err := store.SetActive(key, true, restoreSource); err != nil {
			return ManualMonitorChange{}, fmt.Errorf("resume monitoring for watch %s: %w", storage, err)
		}
		return ManualMonitorChange{
			Changed:   true,
			Monitored: true,
			Action:    "monitor",
			Message:   "monitoring resumed after storage mount",
		}, nil
	default:
		return ManualMonitorChange{}, nil
	}
}
