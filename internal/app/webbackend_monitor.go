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
	action := eventActionMonitor
	if !monitored {
		action = eventActionUnmonitor
	}
	if _, ok := b.entries[name]; !ok {
		msg := fmt.Sprintf(unknownServiceMessageFmt, name)
		b.emitMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if b.store == nil {
		msg := eventMessageMonitoringStateUnavailable
		b.emitMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	priorActive, found, err := b.store.Active(name)
	if err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		b.emitMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if err := b.store.SetActive(name, monitored, state.SourceWeb); err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		b.emitMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if found && priorActive == monitored {
		msg := eventMessageAlreadyMonitored
		if !monitored {
			msg = eventMessageAlreadyPaused
		}
		b.emitMonitorEvent(name, action, eventKindSuppressed, "", msg)
		return nil
	}
	msg := eventMessageMonitoringResumed
	if !monitored {
		msg = eventMessageMonitoringPaused
	}
	b.emitMonitorEvent(name, action, eventKindAction, eventStatusOK, msg)
	return nil
}

// SetWatchMonitored enables or disables monitoring for a host watch.
func (b *WebBackend) SetWatchMonitored(_ context.Context, name string, monitored bool) error {
	action := eventActionMonitor
	if !monitored {
		action = eventActionUnmonitor
	}
	if _, ok := b.watches[name]; !ok {
		msg := fmt.Sprintf("unknown watch %q", name)
		b.emitWatchMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if b.store == nil {
		msg := eventMessageMonitoringStateUnavailable
		b.emitWatchMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	key := watchMonitorKey(name)
	priorActive, found, err := b.store.Active(key)
	if err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		b.emitWatchMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if err := b.store.SetActive(key, monitored, state.SourceWeb); err != nil {
		msg := fmt.Sprintf("%s failed: %v", action, err)
		b.emitWatchMonitorEvent(name, action, eventKindError, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if found && priorActive == monitored {
		msg := eventMessageAlreadyMonitored
		if !monitored {
			msg = eventMessageAlreadyPaused
		}
		b.emitWatchMonitorEvent(name, action, eventKindSuppressed, "", msg)
		return nil
	}
	msg := eventMessageMonitoringResumed
	if !monitored {
		msg = eventMessageMonitoringPaused
	}
	b.emitWatchMonitorEvent(name, action, eventKindAction, eventStatusOK, msg)
	return nil
}

func (b *WebBackend) emitMonitorEvent(service, action, kind, status, message string) {
	if b.emit == nil {
		return
	}
	b.emit(Event{
		Service: service,
		Kind:    kind,
		Action:  action,
		Status:  status,
		Message: message,
	})
}

func (b *WebBackend) emitWatchMonitorEvent(watch, action, kind, status, message string) {
	if b.emit == nil {
		return
	}
	b.emit(Event{
		Watch:   watch,
		Kind:    kind,
		Action:  action,
		Status:  status,
		Message: message,
	})
}
