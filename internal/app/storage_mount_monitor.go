package app

import (
	"sermo/internal/config"
	"sermo/internal/mountctl"
	"sermo/internal/state"
)

// SyncStorageMountMonitoring pauses a storage watch after a successful umount and
// restores it after a later successful mount when the umount created the pause.
// Existing manual unmonitor state is preserved.
func SyncStorageMountMonitoring(store MonitorStore, storage, action string, resultOK bool, monitorMode string, disabled bool, pauseSource, restoreSource string) (ManualMonitorChange, error) {
	if store == nil || disabled || monitorMode == config.MonitorDisabled || !resultOK {
		return ManualMonitorChange{}, nil
	}
	key := watchMonitorKey(storage)
	subject := "watch " + storage
	switch action {
	case mountctl.ActionUmount:
		return syncMonitorPause(store, key, subject, pauseSource, eventMessageMonitoringPausedAfterStorageUmount)
	case mountctl.ActionMount:
		return syncMonitorRestore(store, key, subject, restoreSource,
			eventMessageMonitoringResumedAfterStorageMount, state.IsMountUmountSource)
	default:
		return ManualMonitorChange{}, nil
	}
}
