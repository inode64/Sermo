package app

import (
	"errors"
	"sermo/internal/state"
)

var errWatchAlarmSilenced = errors.New("watch alarm silenced")

func (n *EventNotifier) allowsAlarmRecord(rec state.EventNotifyRecord) bool {
	parts, ok := parseEventNotifyKey(rec.IncidentKey)
	if !ok {
		return true
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
