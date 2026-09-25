package app

import (
	"context"
	"fmt"
	"sermo/internal/state"
	"sermo/internal/web"
	"time"
)

// monitorStateView is the persisted monitor state rendered by services and
// watches. ok is false when there is no store or no record.
type monitorStateView struct {
	active    bool
	source    string
	changedAt time.Time
}

func (v monitorStateView) changedAtText() string {
	if v.changedAt.IsZero() {
		return ""
	}
	return v.changedAt.UTC().Format(time.RFC3339)
}

func (b *WebBackend) monitorView(key string) (monitorStateView, bool) {
	if b.store == nil {
		return monitorStateView{}, false
	}
	rec, found, err := b.store.MonitorState(key)
	if err != nil || !found {
		return monitorStateView{}, false
	}
	return monitorStateView{active: rec.Active, source: rec.Source, changedAt: rec.UpdatedAt}, true
}

func (b *WebBackend) monitorRecords() map[string]state.MonitorRecord {
	if source, ok := b.store.(interface {
		MonitorStates() (map[string]state.MonitorRecord, error)
	}); ok {
		records, err := source.MonitorStates()
		if err == nil {
			return records
		}
	}
	return nil
}

func (b *WebBackend) monitorViewFrom(records map[string]state.MonitorRecord, key string) (monitorStateView, bool) {
	if records == nil {
		return b.monitorView(key)
	}
	record, ok := records[key]
	return monitorStateView{active: record.Active, source: record.Source, changedAt: record.UpdatedAt}, ok
}

// MonitoringStatus returns how many services are monitored versus paused.
func (b *WebBackend) MonitoringStatus(_ context.Context) web.MonitoringStatus {
	total := 0
	monitored := 0
	records := b.monitorRecords()
	for _, name := range b.order {
		if _, ok := b.enabledEntry(name); !ok {
			continue
		}
		total++
		active := true
		if monitoredState, ok := b.monitorViewFrom(records, name); ok {
			active = monitoredState.active
		}
		if active {
			monitored++
		}
	}
	return web.MonitoringStatus{
		Total:     total,
		Monitored: monitored,
		Paused:    total - monitored,
	}
}

// SetMonitored enables or disables monitoring for a service.
func (b *WebBackend) SetMonitored(_ context.Context, name string, monitored bool) error {
	emit := func(action, kind, status, message string) {
		b.emitMonitorEvent(name, action, kind, status, message)
	}
	_, known := b.entries[name]
	return b.setMonitoredTarget(known, name, fmt.Sprintf(unknownServiceMessageFmt, name), monitored, emit)
}

// SetWatchMonitored enables or disables monitoring for a host watch.
func (b *WebBackend) SetWatchMonitored(_ context.Context, name string, monitored bool) error {
	emit := func(action, kind, status, message string) {
		b.emitWatchMonitorEvent(name, action, kind, status, message)
	}
	_, known := b.watches[name]
	return b.setMonitoredTarget(known, WatchMonitorKey(name), fmt.Sprintf(unknownWatchMessageFmt, name), monitored, emit)
}

// setMonitoredTarget rejects an unknown target with the emitted error and
// otherwise flips its monitoring state; the lookup+emit shape shared by the
// service and watch toggles.
func (b *WebBackend) setMonitoredTarget(known bool, key, unknownMsg string, monitored bool, emit monitorEventEmitter) error {
	if !known {
		emit(monitorAction(monitored), eventKindError, "", unknownMsg)
		return fmt.Errorf("%s", unknownMsg)
	}
	return b.setMonitoringState(key, monitored, emit)
}

type monitorEventEmitter func(action, kind, status, message string)

func (b *WebBackend) setMonitoringState(key string, monitored bool, emit monitorEventEmitter) error {
	action := monitorAction(monitored)
	if b.store == nil {
		msg := eventMessageMonitoringStateUnavailable
		emit(action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	changed, err := ApplyMonitorTransition(b.store, key, monitored, state.SourceWeb)
	if err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		emit(action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if !changed {
		emit(action, eventKindSuppressed, "", monitorMessage(monitored, eventMessageAlreadyMonitored, eventMessageAlreadyPaused))
		return nil
	}
	emit(action, eventKindAction, eventStatusOK, monitorMessage(monitored, eventMessageMonitoringResumed, eventMessageMonitoringPaused))
	return nil
}

func monitorAction(monitored bool) string {
	if monitored {
		return eventActionMonitor
	}
	return eventActionUnmonitor
}

func monitorMessage(monitored bool, active, paused string) string {
	if monitored {
		return active
	}
	return paused
}

func (b *WebBackend) emitMonitorEvent(service, action, kind, status, message string) {
	b.emitMonitorSubjectEvent(Event{Service: service}, action, kind, status, message)
}

func (b *WebBackend) emitWatchMonitorEvent(watch, action, kind, status, message string) {
	b.emitMonitorSubjectEvent(Event{Watch: watch}, action, kind, status, message)
}

// emitMonitorSubjectEvent fills the shared monitor-event fields onto an event
// that already carries its subject (Service or Watch) and emits it.
func (b *WebBackend) emitMonitorSubjectEvent(ev Event, action, kind, status, message string) {
	ev.Kind = kind
	ev.Action = action
	ev.Status = status
	ev.Message = message
	emitSafe(b.emit, ev)
}
