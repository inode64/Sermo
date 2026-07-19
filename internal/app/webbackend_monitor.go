package app

import (
	"context"
	"fmt"
	"sermo/internal/state"
	"sermo/internal/web"
	"time"
)

// monitorView reads one monitor record and renders the view fields services
// and watches share: active flag, source, and the RFC3339 change time ("" when
// unknown). ok is false when there is no store or no record.
func (b *WebBackend) monitorView(key string) (active bool, source, changedAt string, ok bool) {
	if b.store == nil {
		return false, "", "", false
	}
	rec, found, err := b.store.MonitorState(key)
	if err != nil || !found {
		return false, "", "", false
	}
	changed := ""
	if !rec.UpdatedAt.IsZero() {
		changed = rec.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return rec.Active, rec.Source, changed, true
}

// MonitoringStatus returns how many services are monitored versus paused.
func (b *WebBackend) MonitoringStatus(_ context.Context) web.MonitoringStatus {
	total := 0
	monitored := 0
	for _, name := range b.order {
		e := b.entries[name]
		if e == nil || e.disabled {
			continue
		}
		total++
		active := true
		if monitoredState, _, _, ok := b.monitorView(name); ok {
			active = monitoredState
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
	return b.setMonitoredTarget(known, watchMonitorKey(name), fmt.Sprintf(unknownWatchMessageFmt, name), monitored, emit)
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
	priorActive, found, err := b.store.Active(key)
	if err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		emit(action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if err := b.store.SetActive(key, monitored, state.SourceWeb); err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		emit(action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if found && priorActive == monitored {
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
	if b.emit == nil {
		return
	}
	ev.Kind = kind
	ev.Action = action
	ev.Status = status
	ev.Message = message
	b.emit(ev)
}
