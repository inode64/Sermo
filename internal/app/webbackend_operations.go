package app

import (
	"context"
	"fmt"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/web"
	"time"
)

// SetPanic enables or disables the daemon-wide panic mode, persisting the flag
// so it survives daemon restarts. The running workers pick up the change within
// the panic gate's refresh window.
func (b *WebBackend) SetPanic(_ context.Context, on bool) web.ActionResult {
	action := eventActionPanicOff
	if on {
		action = eventActionPanicOn
	}
	if b.store == nil {
		msg := "panic mode state is unavailable"
		b.emitMonitorEvent("", action, eventKindError, "", msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	prior, found, err := b.store.Panic()
	if err != nil {
		msg := fmt.Sprintf("panic mode failed: %v", err)
		b.emitMonitorEvent("", action, eventKindError, "", msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if err := b.store.SetPanic(on, state.SourceWeb); err != nil {
		msg := fmt.Sprintf("panic mode failed: %v", err)
		b.emitMonitorEvent("", action, eventKindError, "", msg)
		return web.ActionResult{OK: false, Message: msg}
	}
	if found && prior.On == on {
		msg := "panic mode already on"
		if !on {
			msg = "panic mode already off"
		}
		b.emitMonitorEvent("", action, eventKindSuppressed, "", msg)
		return web.ActionResult{OK: true, Message: msg}
	}
	msg := "panic mode enabled: hooks, alerts and automatic remediation suspended"
	if !on {
		msg = "panic mode disabled: normal operation resumed"
	}
	b.emitMonitorEvent("", action, eventKindAction, eventStatusOK, msg)
	return web.ActionResult{OK: true, Message: msg}
}

// Operations returns current operation-slot usage and the active-user count.
func (b *WebBackend) Operations(_ context.Context) web.OperationSlots {
	users := notify.ActiveUserCount()
	if b.opGate == nil {
		return web.OperationSlots{ActiveUsers: users}
	}
	inUse, total := b.opGate.Usage()
	return web.OperationSlots{InUse: inUse, Total: total, ActiveUsers: users}
}

// operateError emits the error event for a rejected service action and returns
// the matching failed ActionResult.
func (b *WebBackend) operateError(name, action, msg string) web.ActionResult {
	if b.emit != nil {
		b.emit(Event{Service: name, Kind: eventKindError, Action: action, Message: msg})
	}
	return web.ActionResult{OK: false, Message: msg}
}

// Operate runs a start/stop/restart/reload/resume action on a service.
func (b *WebBackend) Operate(ctx context.Context, name, action string, opts web.OperateOpts) web.ActionResult {
	e := b.entries[name]
	if e == nil {
		return b.operateError(name, action, unknownServiceMessage+name)
	}
	if e.disabled {
		return b.operateError(name, action, serviceSubjectPrefix+name+" is disabled in configuration")
	}
	if action == string(rules.ActionReload) && !e.canReload {
		return b.operateError(name, action, serviceSubjectPrefix+name+" does not support reload")
	}

	var r operation.Result
	if opts.NoCascade || action == string(rules.ActionReload) || action == string(rules.ActionResume) || len(e.alsoApply) == 0 {
		r = b.operationResultWithMonitor(ctx, name, action)
	} else {
		lookup := func(svc string) []string {
			ent := b.entries[svc]
			if ent == nil {
				return nil
			}
			return ent.alsoApply
		}
		c := cascader{
			op:     b.operationResultWithMonitor,
			lookup: lookup,
			emit:   b.emit,
			sleep:  time.Sleep,
		}
		r = c.run(ctx, name, action)
	}
	return webActionResultFrom(r, name, action)
}

func webActionResultFrom(r operation.Result, name, action string) web.ActionResult {
	if r.Action == "" && action != "" {
		r.Action = action
	}
	if r.Service == "" {
		r.Service = name
	}
	msg := r.Message
	if msg == "" {
		msg = string(r.Status)
	}
	return web.ActionResult{OK: r.OK(), Message: msg}
}

func (b *WebBackend) operationResultWithMonitor(ctx context.Context, name, action string) operation.Result {
	if err := beginOperationSettling(b.operationSettling, name, action, state.SourceWeb); err != nil {
		b.emitMonitorEvent(name, action, eventKindError, "", err.Error())
	}
	r := b.operationResult(ctx, name, action)
	activeAfterPostflightFailure := b.activeAfterPostflightFailure(ctx, name, action, r)
	change, err := CompleteManualOperation(b.store, b.operationSettling, name, action, r, nil, ManualOperationSources{Stop: state.SourceWebManualStop, Restore: state.SourceWeb, Settling: state.SourceWeb}, activeAfterPostflightFailure)
	if err != nil {
		b.emitMonitorEvent(name, action, eventKindError, "", err.Error())
	} else if change.Changed {
		b.emitMonitorEvent(name, change.Action, eventKindAction, eventStatusOK, change.Message)
	}
	return r
}

func (b *WebBackend) activeAfterPostflightFailure(ctx context.Context, name, action string, result operation.Result) bool {
	if result.Status != operation.ResultPostflightFailed || !ManualActionCanRemainActiveAfterPostflightFailure(action) {
		return false
	}
	e := b.entries[name]
	if e == nil {
		return false
	}
	return e.backendStatus(ctx, b.webNow()) == string(servicemgr.StatusActive)
}

func (b *WebBackend) operationResult(ctx context.Context, name, action string) operation.Result {
	e := b.entries[name]
	if e == nil {
		return operation.Result{Service: name, Action: action, Status: operation.ResultFailed, Message: unknownServiceMessage + name}
	}
	if e.disabled {
		return operation.Result{Service: name, Action: action, Status: operation.ResultFailed, Message: serviceSubjectPrefix + name + " is disabled in configuration"}
	}
	timeout := b.operationTimeout
	if timeout <= 0 {
		timeout = operation.DefaultOperationTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	run := func(ctx context.Context) operation.Result {
		return e.engine.Do(ctx, action)
	}
	var r operation.Result
	if b.opGate != nil {
		r = b.opGate.Run(ctx, name, action, run)
	} else {
		r = run(ctx)
	}
	if r.Action == "" && action != "" {
		r.Action = action
	}
	if r.Service == "" {
		r.Service = name
	}
	e.invalidateStatusCache()
	return r
}

// CompactState prunes old persisted history and vacuums the state database.
func (b *WebBackend) CompactState(ctx context.Context, before time.Time) web.StateCompactResult {
	maint, ok := b.store.(stateMaintainer)
	if !ok || maint == nil {
		return web.StateCompactResult{OK: false, Message: "state store unavailable"}
	}
	now := b.webNow()
	if before.IsZero() {
		before = now.Add(-state.DefaultHistoryRetention)
	}
	timeout := b.operationTimeout
	if timeout <= 0 {
		timeout = b.defaultTimeout
	}
	if timeout <= 0 {
		timeout = operation.DefaultOperationTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := maint.PruneHistory(before)
	if err != nil {
		return web.StateCompactResult{OK: false, Message: "prune state history: " + err.Error()}
	}
	if err := maint.Compact(ctx); err != nil {
		return web.StateCompactResult{OK: false, Message: "compact state database: " + err.Error()}
	}
	return web.StateCompactResult{
		OK:             true,
		Pruned:         result.Rows,
		Before:         before.UTC().Format(time.RFC3339),
		SLA:            result.SLA,
		Measurements:   result.Measurements,
		Metrics:        result.Metrics,
		DaemonMetrics:  result.DaemonMetrics,
		ServiceMetrics: result.ServiceMetrics,
		ProcessUptime:  result.ProcessUptime,
		Events:         result.Events,
		Vacuum:         true,
	}
}

// ControlRAID pauses or resumes a configured md reconstruction. Pause requires
// the array name in confirmation; the browser obtains it only after its second
// confirmation prompt and the backend independently validates it again.
func (b *WebBackend) ControlRAID(ctx context.Context, name, action, confirmation string) web.ActionResult {
	w := b.watches[name]
	if w == nil {
		return web.ActionResult{Message: fmt.Sprintf(unknownWatchMessageFmt, name)}
	}
	if w.disabled || !w.raidControl || w.checkType != checks.CheckTypeRAID {
		return web.ActionResult{Message: fmt.Sprintf("watch %q has no RAID pause/resume control configured", name)}
	}
	array := cfgval.String(w.check[checks.CheckKeyArray])
	if array == "" {
		return web.ActionResult{Message: fmt.Sprintf("watch %q has no RAID array", name)}
	}
	if action == RaidControlPause && confirmation != array {
		msg := fmt.Sprintf("confirm RAID array %q before pausing reconstruction", array)
		b.emitWatchMonitorEvent(name, eventActionRAIDPause, eventKindSuppressed, eventStatusBlocked, msg)
		return web.ActionResult{Message: msg}
	}
	result := ControlRAID(ctx, b.cfg.Global.RuntimeDir(), array, action, b.operationTimeout)
	kind, status := eventKindAction, eventStatusOK
	if !result.OK {
		kind, status = eventKindError, eventStatusFailed
	}
	eventAction := eventActionRAIDPause
	if action == RaidControlResume {
		eventAction = eventActionRAIDResume
	}
	b.emitWatchMonitorEvent(name, eventAction, kind, status, result.Message)
	return web.ActionResult{OK: result.OK, Message: result.Message}
}
