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
	return parts[0] != eventNotifyDimensionWatch || n.WatchAlarms.allowsNotification(parts[1], rec.AlarmEpoch)
}
