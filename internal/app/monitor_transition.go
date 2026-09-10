package app

import "fmt"

// ApplyMonitorTransition applies one explicit monitoring-state request. A
// repeated request is a true no-op: it preserves the existing source and
// timestamp instead of rewriting persistence metadata.
func ApplyMonitorTransition(store MonitorStateStore, key string, monitored bool, source string) (bool, error) {
	record, found, err := store.MonitorState(key)
	if err != nil {
		return false, fmt.Errorf("read monitoring state for %s: %w", key, err)
	}
	wasMonitored := true
	if found {
		wasMonitored = record.Active
	}
	if wasMonitored == monitored {
		return false, nil
	}
	if err := store.SetActive(key, monitored, source); err != nil {
		return false, fmt.Errorf("set monitoring state for %s: %w", key, err)
	}
	return true, nil
}
