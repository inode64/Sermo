package app

import (
	"errors"
	"path"

	"sermo/internal/state"
)

var errWatchAlarmSilenced = errors.New("watch alarm silenced")

func (n *EventNotifier) allowsAlarmRecord(rec state.EventNotifyRecord) bool {
	parts, ok := parseEventNotifyKey(rec.IncidentKey)
	if !ok {
		return true
	}
	if n.excluded(parts[0], parts[1]) {
		return false
	}
	// An active service episode may already be queued, or due for a
	// reminder, when monitoring is paused. Keep its confirmed receipt so
	// a later recovery still reaches the channels that heard the opening.
	if rec.Active && n.scope != nil && parts[0] == eventNotifyDimensionService {
		paused := n.scope.services[parts[1]].paused
		return paused == nil || !paused()
	}
	return parts[0] != eventNotifyDimensionWatch || n.WatchAlarms.allowsNotification(parts[1], rec.AlarmEpoch)
}

func (n *EventNotifier) excluded(dimension, name string) bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, pattern := range n.policy.Exclude {
		if matched, _ := path.Match(pattern, dimension+":"+name); matched {
			return true
		}
	}
	return false
}
