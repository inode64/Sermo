package app

import "fmt"

// MonitorTransition describes the effective state after an explicit monitor or
// unmonitor request. Changed is false when the requested state was already in
// effect; an absent record has the default monitored state.
type MonitorTransition struct {
	Changed bool
}

// ApplyMonitorTransition applies one explicit monitoring-state request. A
// repeated request is a true no-op: it preserves the existing source and
// timestamp instead of rewriting persistence metadata.
func ApplyMonitorTransition(store MonitorStateStore, key string, monitored bool, source string) (MonitorTransition, error) {
	record, found, err := store.MonitorState(key)
	if err != nil {
		return MonitorTransition{}, fmt.Errorf("read monitoring state for %s: %w", key, err)
	}
	wasMonitored := true
	if found {
		wasMonitored = record.Active
	}
	result := MonitorTransition{}
	if wasMonitored == monitored {
		return result, nil
	}
	if err := store.SetActive(key, monitored, source); err != nil {
		return MonitorTransition{}, fmt.Errorf("set monitoring state for %s: %w", key, err)
	}
	result.Changed = true
	return result, nil
}
