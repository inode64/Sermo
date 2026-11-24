package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"sermo/internal/notify"
	"sermo/internal/state"
	"sermo/internal/units"
)

// Bounds of an operator's notification mute. Every mute expires on its own, so
// one forgotten after a maintenance window cannot hide the next outage.
const (
	DefaultNotificationMute = time.Hour
	MinNotificationMute     = time.Minute
	MaxNotificationMute     = 24 * time.Hour
)

// NotifyMuteStore reads operator notification mutes. Satisfied by
// *state.Store; the keys are the monitor-state keys (a service's name,
// WatchMonitorKey for a watch) and NotifierMuteKey for a notifier.
type NotifyMuteStore interface {
	NotificationMute(key string) (state.NotificationMuteRecord, bool, error)
}

// ValidateNotificationMute rejects a mute duration outside the bounds.
func ValidateNotificationMute(d time.Duration) error {
	if d < MinNotificationMute || d > MaxNotificationMute {
		return fmt.Errorf("notification mute must last from %s to %s, got %s",
			units.HumanizeDuration(MinNotificationMute), units.HumanizeDuration(MaxNotificationMute), d)
	}
	return nil
}

// notifyMuteKeys are the mute keys a watch's notifications follow: its own and,
// for a service watch ("<service>:<watch>"), its service's, so muting a service
// silences everything it reports.
func notifyMuteKeys(watch string) []string {
	keys := []string{WatchMonitorKey(watch)}
	if service, _, ok := strings.Cut(watch, serviceWatchNameSeparator); ok && service != "" {
		keys = append(keys, service)
	}
	return keys
}

// NotifierMuteKey is the mute key of one notifier: an operator's pause of that
// channel for every service and watch on the host.
func NotifierMuteKey(name string) string {
	return "notifier:" + name
}

// notifyMute reads the operator mutes that hold over one target's deliveries:
// the target's own and, per delivery, the notifier's. It reads the store on
// every call, so a mute or unmute from the CLI takes effect on the next
// notification without a daemon reload, and it fails open: a store error or a
// missing row lets the notification go.
type notifyMute struct {
	store NotifyMuteStore
	now   func() time.Time
	keys  []string
}

func notifyMuteGate(store NotifyMuteStore, now func() time.Time, keys ...string) *notifyMute {
	if store == nil || len(keys) == 0 {
		return nil
	}
	return &notifyMute{store: store, now: clockOrNow(now), keys: keys}
}

// target reports whether the target's notifications are muted, and until
// when (the latest until among its keys).
func (m *notifyMute) target() (bool, time.Time) {
	if m == nil {
		return false, time.Time{}
	}
	return mutedUntil(m.store, m.now(), m.keys)
}

// notifier reports whether the named notifier is muted, whatever the target.
func (m *notifyMute) notifier(name string) (bool, time.Time) {
	if m == nil {
		return false, time.Time{}
	}
	return mutedUntil(m.store, m.now(), []string{NotifierMuteKey(name)})
}

func mutedUntil(store NotifyMuteStore, now time.Time, keys []string) (bool, time.Time) {
	var until time.Time
	for _, key := range keys {
		rec, found, err := store.NotificationMute(key)
		if err != nil || !found || !rec.MutedAt(now) {
			continue
		}
		if rec.Until.After(until) {
			until = rec.Until
		}
	}
	return !until.IsZero(), until
}

type notifyMuteContextKey struct{}

// withNotifyMute carries a target's mutes to every delivery its cycle makes:
// deliverNotification reads them, and so do the sites that must not record a
// delivery that never happened.
func withNotifyMute(ctx context.Context, mute *notifyMute) context.Context {
	if mute == nil {
		return ctx
	}
	return context.WithValue(ctx, notifyMuteContextKey{}, mute)
}

func notifyMutedFrom(ctx context.Context) (bool, time.Time) {
	mute, _ := ctx.Value(notifyMuteContextKey{}).(*notifyMute)
	return mute.target()
}

func notifierMutedFrom(ctx context.Context, name string) (bool, time.Time) {
	mute, _ := ctx.Value(notifyMuteContextKey{}).(*notifyMute)
	return mute.notifier(name)
}

// notificationsMutedError is the delivery report of a notification an
// operator's mute withheld, the target's or (notifier set) the notifier's own;
// deliveryReport records it as notify-suppressed.
type notificationsMutedError struct {
	until    time.Time
	notifier bool
}

func (e notificationsMutedError) Error() string {
	subject := "notifications"
	if e.notifier {
		subject = "notifier"
	}
	return subject + " muted until " + e.until.Local().Format(time.DateTime)
}

// muted combines configured exclusions with temporary operator mutes.
func (n *EventNotifier) muted(e Event) bool {
	if n.excluded(eventNotifySubject(e)) {
		return true
	}
	if n.NotifyMutes == nil {
		return false
	}
	var keys []string
	switch {
	case e.Service != "":
		keys = []string{e.Service}
	case e.Watch != "":
		keys = notifyMuteKeys(e.Watch)
	default:
		return false
	}
	muted, _ := mutedUntil(n.NotifyMutes, n.now(), keys)
	return muted
}

// notifierMuted reports whether an operator muted target itself: nothing
// reaches it, whatever reports.
func (n *EventNotifier) notifierMuted(target notify.Notifier) bool {
	if n.NotifyMutes == nil {
		return false
	}
	muted, _ := mutedUntil(n.NotifyMutes, n.now(), []string{NotifierMuteKey(target.Name())})
	return muted
}

// unmutedTargets drops the notifiers an operator muted.
func (n *EventNotifier) unmutedTargets(targets []notify.Notifier) []notify.Notifier {
	return slices.DeleteFunc(targets, n.notifierMuted)
}

// recordMuted reports whether a reminder's incident belongs to a muted target.
// The reminder waits: it goes out on the first scan after the mute ends.
func (n *EventNotifier) recordMuted(rec state.EventNotifyRecord) bool {
	parts, ok := parseEventNotifyKey(rec.IncidentKey)
	if !ok || len(parts) < 2 {
		return false
	}
	switch parts[0] {
	case eventNotifyDimensionService:
		return n.muted(Event{Service: parts[1]})
	case eventNotifyDimensionWatch:
		return n.muted(Event{Watch: parts[1]})
	}
	return false
}

// NotifyMuteWriter persists notification mutes. Satisfied by *state.Store.
type NotifyMuteWriter interface {
	SetNotificationMute(key string, until time.Time, source string) error
	ClearNotificationMute(key string) (bool, error)
}

// ApplyNotificationMute mutes key's notifications for d from now, or unmutes
// them when d is zero. It returns the mute's end (zero after an unmute) and,
// for an unmute, whether a mute row was removed. A new mute replaces an
// earlier one, shorter or not: the operator's last word wins.
func ApplyNotificationMute(store NotifyMuteWriter, key string, d time.Duration, source string, now time.Time) (time.Time, bool, error) {
	if d == 0 {
		removed, err := store.ClearNotificationMute(key)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("unmute notifications for %s: %w", key, err)
		}
		return time.Time{}, removed, nil
	}
	if err := ValidateNotificationMute(d); err != nil {
		return time.Time{}, false, err
	}
	until := now.Add(d).Truncate(time.Second)
	if err := store.SetNotificationMute(key, until, source); err != nil {
		return time.Time{}, false, fmt.Errorf("mute notifications for %s: %w", key, err)
	}
	return until, true, nil
}

// NotificationMuteMessage is the operator-facing result of a mute or unmute.
func NotificationMuteMessage(until time.Time, changed bool) string {
	switch {
	case !until.IsZero():
		return "notifications muted until " + until.Local().Format(time.DateTime)
	case changed:
		return "notifications unmuted"
	default:
		return "notifications were not muted"
	}
}
