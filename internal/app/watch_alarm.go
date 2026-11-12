package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/severity"
	"sermo/internal/state"
)

// WatchAlarmStore persists alarm control separately from monitoring and health.
type WatchAlarmStore interface {
	WatchAlarmState(name string) (state.WatchAlarmRecord, error)
	SetWatchAlarmState(name string, record state.WatchAlarmRecord) error
}

type watchAlarmEntry struct {
	mu     sync.Mutex
	loaded bool
	dirty  bool
	record state.WatchAlarmRecord
}

// WatchAlarms is shared by cycles, operator requests and notification delivery.
// Per-watch locks keep one watch's storage wait independent of other watches.
type WatchAlarms struct {
	mu      sync.Mutex
	entries map[string]*watchAlarmEntry
	store   WatchAlarmStore
	report  func(error)
	now     func() time.Time
}

// NewWatchAlarms constructs a controller that survives configuration generations.
func NewWatchAlarms(store WatchAlarmStore, report func(error)) *WatchAlarms {
	return &WatchAlarms{entries: map[string]*watchAlarmEntry{}, store: store, report: report, now: time.Now}
}

func (a *WatchAlarms) entry(name string) *watchAlarmEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.entries[name] == nil {
		a.entries[name] = &watchAlarmEntry{}
	}
	return a.entries[name]
}

func (a *WatchAlarms) load(name string, e *watchAlarmEntry) error {
	if e.loaded {
		return nil
	}
	if a.store != nil {
		rec, err := a.store.WatchAlarmState(name)
		if err != nil {
			return fmt.Errorf("load watch alarm: %w", err)
		}
		e.record = rec
	}
	e.loaded = true
	return nil
}

// Observe advances the revision only on a confirmed state change. A healthy
// slot disappears so per-statement watches cannot accumulate completed queries.
func (a *WatchAlarms) Observe(name, slot, configID string, observed state.WatchAlarmSlot) {
	if a == nil {
		return
	}
	e := a.entry(name)
	e.mu.Lock()
	err := a.load(name, e)
	if err == nil {
		changed := e.record.ConfigID != configID
		if changed {
			e.record.Slots = nil
			e.record.ConfigID = configID
		}
		previous, found := e.record.Slots[slot]
		if observed.Active {
			changed = changed || !found || previous != observed
			if e.record.Slots == nil {
				e.record.Slots = map[string]state.WatchAlarmSlot{}
			}
			e.record.Slots[slot] = observed
		} else {
			changed = changed || found
			delete(e.record.Slots, slot)
		}
		if changed {
			e.record.Revision = rand.Text()
			e.record.Silenced = false
			e.record.SilencedAt = time.Time{}
			e.record.Source = ""
			e.dirty = true
		}
		if e.dirty && a.store != nil {
			err = a.store.SetWatchAlarmState(name, e.record)
			e.dirty = err != nil
		}
	}
	e.mu.Unlock()
	if err != nil && a.report != nil {
		a.report(err)
	}
}

type watchAlarmStatus struct {
	Active     bool
	Revision   string
	Silenced   bool
	SilencedAt time.Time
	Source     string
}

// status copies the operator-visible state without exposing the mutable slots.
func (a *WatchAlarms) status(name string) watchAlarmStatus {
	if a == nil {
		return watchAlarmStatus{}
	}
	e := a.entry(name)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := a.load(name, e); err != nil {
		return watchAlarmStatus{}
	}
	r := e.record
	return watchAlarmStatus{
		Active: len(r.Slots) > 0, Revision: r.Revision, Silenced: r.Silenced,
		SilencedAt: r.SilencedAt, Source: r.Source,
	}
}

// Silence refuses an obsolete page even if the watch has since returned to the
// same health: the revision identifies the occurrence, not just its severity.
func (a *WatchAlarms) Silence(name, revision, source string) (bool, error) {
	if a == nil || a.store == nil {
		return false, errors.New("watch alarm persistence unavailable")
	}
	e := a.entry(name)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := a.load(name, e); err != nil {
		return false, err
	}
	if revision == "" || revision != e.record.Revision || len(e.record.Slots) == 0 {
		return false, errors.New("watch alarm changed; refresh and try again")
	}
	if e.record.Silenced {
		return false, nil
	}
	rec := e.record
	rec.Slots = maps.Clone(rec.Slots)
	rec.Silenced, rec.SilencedAt, rec.Source = true, a.now(), source
	rec.Epoch = rand.Text()
	if err := a.store.SetWatchAlarmState(name, rec); err != nil {
		return false, fmt.Errorf("silence watch alarm: %w", err)
	}
	e.record, e.dirty = rec, false
	return true, nil
}

// notificationEpoch invalidates queued deliveries when an operator silences.
// Rearming preserves it: only new deliveries may announce the changed state.
func (a *WatchAlarms) notificationEpoch(name string) (string, bool) {
	if a == nil || name == "" {
		return "", true
	}
	e := a.entry(name)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := a.load(name, e); err != nil {
		return "", true
	}
	return e.record.Epoch, !e.record.Silenced
}

func (a *WatchAlarms) allowsNotification(name, epoch string) bool {
	current, allowed := a.notificationEpoch(name)
	return allowed && current == epoch
}

func (w *Watch) observeAlarm(res checks.Result, firing bool, level severity.Level) {
	if w.App != "" || res.Observation() == checks.ObservationSkipped {
		return
	}
	w.Alarms.Observe(w.Name, w.runtimeStateSlot(), w.configID, state.WatchAlarmSlot{
		Active: firing, Unavailable: res.Observation() == checks.ObservationUnavailable,
		Severity: level.Resolved().String(), Health: cfgval.String(res.Data[checks.DataKeyHealth]),
	})
}

type watchAlarmContextKey struct{}

type watchAlarmScope struct {
	alarms           *WatchAlarms
	name             string
	configID         string
	deliveryEpoch    string
	deliveryCaptured bool
}

func watchAlarmDeliveryAllowed(ctx context.Context) bool {
	scope, _ := ctx.Value(watchAlarmContextKey{}).(watchAlarmScope)
	_, allowed := scope.notificationEpoch()
	return allowed
}

func (w *Watch) alarmContext(ctx context.Context) context.Context {
	if w.App != "" {
		return ctx
	}
	return context.WithValue(ctx, watchAlarmContextKey{}, watchAlarmScope{alarms: w.Alarms, name: w.Name, configID: w.configID})
}

func observeWatchAlarm(ctx context.Context, slot string, observed state.WatchAlarmSlot) {
	scope, _ := ctx.Value(watchAlarmContextKey{}).(watchAlarmScope)
	scope.alarms.Observe(scope.name, slot, scope.configID, observed)
}

// captureWatchAlarmDelivery pins the notification before hooks or actions can
// block. A later silence invalidates it even if another metric rearms the watch.
func captureWatchAlarmDelivery(ctx context.Context) context.Context {
	scope, _ := ctx.Value(watchAlarmContextKey{}).(watchAlarmScope)
	if scope.alarms == nil || scope.deliveryCaptured {
		return ctx
	}
	scope.deliveryEpoch, _ = scope.alarms.notificationEpoch(scope.name)
	scope.deliveryCaptured = true
	return context.WithValue(ctx, watchAlarmContextKey{}, scope)
}

func (s watchAlarmScope) notificationEpoch() (string, bool) {
	if s.deliveryCaptured {
		return s.deliveryEpoch, s.alarms.allowsNotification(s.name, s.deliveryEpoch)
	}
	return s.alarms.notificationEpoch(s.name)
}
