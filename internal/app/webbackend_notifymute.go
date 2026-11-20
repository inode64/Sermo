package app

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/state"
	"sermo/internal/web"
)

// SetServiceNotifyMute mutes a service's notifications for d, or unmutes them
// when d is zero. Its service watches follow the service's mute.
func (b *WebBackend) SetServiceNotifyMute(_ context.Context, name string, d time.Duration) error {
	_, known := b.entries[name]
	return b.setNotifyMute(known, name, fmt.Sprintf(unknownServiceMessageFmt, name), d, func(action, kind, status, message string) {
		b.emitMonitorEvent(name, action, kind, status, message)
	})
}

// SetWatchNotifyMute mutes a watch's notifications for d, or unmutes them when
// d is zero.
func (b *WebBackend) SetWatchNotifyMute(_ context.Context, name string, d time.Duration) error {
	_, known := b.watches[name]
	return b.setNotifyMute(known, WatchMonitorKey(name), fmt.Sprintf(unknownWatchMessageFmt, name), d, func(action, kind, status, message string) {
		b.emitWatchMonitorEvent(name, action, kind, status, message)
	})
}

// SetNotifierNotifyMute mutes one notifier for d, or unmutes it when d is
// zero: nothing the daemon monitors reaches it meanwhile, whatever reports.
func (b *WebBackend) SetNotifierNotifyMute(_ context.Context, name string, d time.Duration) error {
	return b.setNotifyMute(b.notifiers[name] != nil, NotifierMuteKey(name), "unknown notifier "+name, d, func(action, kind, status, message string) {
		b.emitMonitorSubjectEvent(Event{}, action, kind, status, "notifier "+name+": "+message)
	})
}

// setNotifyMute applies one mute request. A refused request is audited as a
// blocked action, like a refused alarm silence: an error event would itself
// reach event_notify and page someone over an operator's own request.
func (b *WebBackend) setNotifyMute(known bool, key, unknownMsg string, d time.Duration, emit monitorEventEmitter) error {
	action := web.APIActionMute
	if d == 0 {
		action = web.APIActionUnmute
	}
	refuse := func(msg string) error {
		emit(action, eventKindAction, eventStatusBlocked, msg)
		return fmt.Errorf("%s", msg)
	}
	writer, ok := b.store.(NotifyMuteWriter)
	switch {
	case !known:
		return refuse(unknownMsg)
	case !ok:
		return refuse("notification mute state is unavailable")
	}
	until, changed, err := ApplyNotificationMute(writer, key, d, state.SourceWeb, b.webNow())
	if err != nil {
		return refuse(err.Error())
	}
	msg := NotificationMuteMessage(until, changed)
	if !changed {
		emit(action, eventKindSuppressed, "", msg)
		return nil
	}
	emit(action, eventKindAction, eventStatusOK, msg)
	return nil
}

// notifyMuteRecords reads every notification mute once for a list snapshot.
func (b *WebBackend) notifyMuteRecords() map[string]state.NotificationMuteRecord {
	source, ok := b.store.(interface {
		NotificationMutes() (map[string]state.NotificationMuteRecord, error)
	})
	if !ok {
		return nil
	}
	records, err := source.NotificationMutes()
	if err != nil {
		return nil
	}
	return records
}

// notifyMuteView is the mute that holds over a target at now: the latest
// until among its keys, its source, and whether it comes only from the
// service a service watch belongs to (which the watch cannot unmute itself).
func notifyMuteView(records map[string]state.NotificationMuteRecord, now time.Time, own string, inherited ...string) (until, source string, fromService bool) {
	var best state.NotificationMuteRecord
	ownHolds := false
	for i, key := range append([]string{own}, inherited...) {
		rec, ok := records[key]
		if !ok || !rec.MutedAt(now) {
			continue
		}
		if i == 0 {
			ownHolds = true
		}
		if rec.Until.After(best.Until) {
			best = rec
		}
	}
	if best.Until.IsZero() {
		return "", "", false
	}
	return best.Until.UTC().Format(time.RFC3339), best.Source, !ownHolds
}
