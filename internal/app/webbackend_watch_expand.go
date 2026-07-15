package app

import (
	"context"
	"fmt"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/operation"
	"sermo/internal/web"
)

// ExpandWatch runs a configured storage watch's then.expand action on demand.
func (b *WebBackend) ExpandWatch(ctx context.Context, name string) web.ActionResult {
	w := b.watches[name]
	if w == nil {
		msg := fmt.Sprintf("unknown watch %q", name)
		b.emitWatchExpandEvent(name, eventKindExpandFailed, eventStatusFailed, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if w.disabled {
		msg := fmt.Sprintf("watch %q is disabled in configuration", name)
		b.emitWatchExpandEvent(name, eventKindExpandSkipped, eventStatusBlocked, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if !isStorageCheckType(w.checkType) {
		msg := fmt.Sprintf("watch %q is %q, not storage", name, w.checkType)
		b.emitWatchExpandEvent(name, eventKindExpandSkipped, eventStatusBlocked, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if w.expand == nil {
		msg := fmt.Sprintf("watch %q has no then.expand action configured", name)
		b.emitWatchExpandEvent(name, eventKindExpandSkipped, eventStatusBlocked, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	path := cfgval.AsString(w.check[checks.CheckKeyPath])
	if path == "" {
		msg := fmt.Sprintf("watch %q storage check has no path", name)
		b.emitWatchExpandEvent(name, eventKindExpandFailed, eventStatusFailed, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	expander := b.expander
	if expander == nil {
		msg := "volume expander is unavailable"
		b.emitWatchExpandEvent(name, eventKindExpandFailed, eventStatusFailed, msg)
		return web.ActionResult{OK: false, Message: msg}
	}

	timeout := b.operationTimeout
	if timeout <= 0 {
		timeout = operation.DefaultOperationTimeout
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := expander.ExpandPath(opCtx, path, w.expand.By)
	if err != nil {
		msg := err.Error()
		b.emitWatchExpandEvent(name, eventKindExpandFailed, eventStatusFailed, msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	msg := expandSuccessMessage(path, res)
	b.emitWatchExpandEvent(name, eventKindExpand, eventStatusOK, msg)
	return web.ActionResult{OK: true, Message: msg}
}

func (b *WebBackend) emitWatchExpandEvent(watch, kind, status, message string) {
	if b.emit == nil {
		return
	}
	b.emit(Event{
		Watch:   watch,
		Kind:    kind,
		Action:  eventActionExpand,
		Status:  status,
		Message: message,
	})
}
