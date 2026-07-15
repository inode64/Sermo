package app

import (
	"context"
	"fmt"
	"sermo/internal/notify"
	"sermo/internal/web"
)

// Notifiers returns the configured notification targets.
func (b *WebBackend) Notifiers(_ context.Context) []web.Notifier {
	if len(b.notifierOrder) == 0 {
		return nil
	}
	usedBy := map[string]int{}
	for _, w := range b.watches {
		if w == nil {
			continue
		}
		for _, n := range w.notifiers {
			usedBy[n]++
		}
	}
	out := make([]web.Notifier, 0, len(b.notifierOrder))
	for _, name := range b.notifierOrder {
		n := b.notifiers[name]
		if n == nil {
			continue
		}
		out = append(out, web.Notifier{
			Name:    n.name,
			Type:    n.typ,
			Enabled: n.enabled,
			Summary: n.summary,
			UsedBy:  usedBy[name],
		})
	}
	return out
}

// TestNotifier sends an explicit operator-requested test message through one
// enabled notifier. It is independent of watch/rule delivery and is bounded by
// the daemon's normal default timeout.
func (b *WebBackend) TestNotifier(ctx context.Context, name string) web.ActionResult {
	configured := b.notifiers[name]
	if configured == nil {
		msg := "unknown notifier " + name
		b.emitNotifierTestEvent(eventKindError, eventStatusFailed, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if !configured.enabled {
		msg := "notifier " + name + " is disabled in configuration"
		b.emitNotifierTestEvent(eventKindError, eventStatusFailed, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	n, ok := b.notifierRegistry[name]
	if !ok {
		msg := "notifier " + name + " is unavailable"
		b.emitNotifierTestEvent(eventKindError, eventStatusFailed, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	timeout := b.defaultTimeout
	if timeout <= 0 {
		timeout = DefaultEngineCheckTimeout
	}
	sendCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := n.Send(sendCtx, notify.TestMessage()); err != nil {
		msg := fmt.Sprintf("send test notification to %s: %v", name, err)
		b.emitNotifierTestEvent(eventKindNotifyFail, eventStatusFailed, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	msg := "test notification sent to " + name
	b.emitNotifierTestEvent(eventKindNotify, eventStatusOK, msg)
	return web.ActionResult{OK: true, Message: msg}
}

func (b *WebBackend) emitNotifierTestEvent(kind, status, message string) {
	if b.emit == nil {
		return
	}
	b.emit(Event{
		Kind:    kind,
		Action:  eventActionNotifierTest,
		Status:  status,
		Message: message,
	})
}
