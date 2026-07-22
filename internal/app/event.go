// Package app is the sermod daemon: a scheduler that runs one independent worker
// per enabled service, each monitoring its service and driving guarded
// remediation through the shared operation engine.
package app

import (
	"log/slog"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/operation"
	"sermo/internal/rules"
)

// Event records what a worker cycle did, for the operator-visible log.
type Event struct {
	Service string
	Watch   string // set for host-watch events (instead of Service)
	App     string // set for installed-application monitoring events (instead of Service/Watch)
	Kind    string // eventKind* value describing the visible event type
	Rule    string
	Action  string
	Status  string
	Message string
	// Output is the bounded stdout/stderr of the failing command behind this
	// event (app probe or service `command` check), shown expandable in the UI so
	// operators can see why it failed. Empty for events without command output.
	Output string
}

// Event kind values for Event.Kind.
const (
	eventKindFailedSuffix = "-failed"

	eventKindAction           = "action"
	eventKindAlert            = string(rules.ActionAlert)
	eventKindError            = "error"
	eventKindHook             = config.WatchThenKeyHook
	eventKindNotify           = rules.RuleFieldNotify
	eventKindDryRun           = "dry-run"
	eventKindFiring           = "firing"
	eventKindRecovered        = "recovered"
	eventKindHookFail         = config.WatchThenKeyHook + eventKindFailedSuffix
	eventKindNotifyFail       = rules.RuleFieldNotify + eventKindFailedSuffix
	eventKindSuppressed       = "suppressed"
	eventKindPanicSuppressed  = "panic-suppressed"
	eventKindNotifySuppressed = "notify-suppressed"
	eventKindCascade          = "cascade"
	eventKindReload           = string(rules.ActionReload)

	eventKindExpand        = config.WatchThenKeyExpand
	eventKindExpandSkipped = "expand-skipped"
	eventKindExpandFailed  = config.WatchThenKeyExpand + eventKindFailedSuffix
	eventKindKill          = config.WatchThenKeyKill
	eventKindKillFailed    = config.WatchThenKeyKill + eventKindFailedSuffix
)

// Event status values for Event.Status — the outcome of an emitted action:
// succeeded, blocked by a guard/lock/cooldown, or failed.
const (
	eventStatusOK      = string(operation.ResultOK)
	eventStatusBlocked = string(operation.ResultBlocked)
	eventStatusFailed  = string(operation.ResultFailed)
	eventStatusRunning = "running"
)

// Event action values emitted by daemon-side monitoring adjustments and web
// actions that are not service operation rule actions.
const (
	eventActionMonitor           = "monitor"
	eventActionUnmonitor         = "unmonitor"
	eventActionExpand            = config.WatchThenKeyExpand
	eventActionProbe             = "probe"
	eventActionRAIDPause         = "pause"
	eventActionRAIDResume        = "resume"
	eventActionReleaseLock       = "release-lock"
	eventActionOperationSettling = "operation-settling"
	eventActionPanicOn           = "panic-on"
	eventActionPanicOff          = "panic-off"
	eventActionReload            = string(rules.ActionReload)
	eventActionNotifierTest      = "test"
)

// Subject prefixes name the entity a warning, event message or monitor label is
// about, e.g. "service <name>: ..." or "watch <name>: ...". They are human-facing
// text only; the persisted monitor-state keys are separate (see WatchMonitorKey).
const (
	serviceSubjectPrefix = "service "
	watchSubjectPrefix   = "watch "
	// watchUnderServiceSubject prefixes a service-embedded watch under its service
	// subject, e.g. "service <svc>: watch <name>".
	watchUnderServiceSubject = ": " + watchSubjectPrefix
)

// Event message values shared by monitor-state transitions.
const (
	eventMessageMonitoringPaused                   = "monitoring paused"
	eventMessageMonitoringResumed                  = "monitoring resumed"
	eventMessageAlreadyPaused                      = "already paused"
	eventMessageAlreadyMonitored                   = "already monitored"
	eventMessageMonitoringStateUnavailable         = "monitoring state is unavailable"
	eventMessageMonitoringPausedAfterManualStop    = "monitoring paused after manual stop"
	eventMessageMonitoringResumedAfterManualStart  = "monitoring resumed after manual start"
	eventMessageMonitoringPausedAfterStorageUmount = "monitoring paused after storage umount"
	eventMessageMonitoringResumedAfterStorageMount = "monitoring resumed after storage mount"
	eventMessageManualProbeStarted                 = "manual probe started"
)

// Event field names are shared by structured logs and JSON event export.
const (
	eventFieldTime    = "time"
	eventFieldService = "service"
	eventFieldWatch   = "watch"
	eventFieldApp     = "app"
	eventFieldKind    = "kind"
	eventFieldRule    = "rule"
	eventFieldAction  = "action"
	eventFieldStatus  = "status"
	eventFieldMessage = "message"
	eventFieldOutput  = "output"
)

// resultOutput extracts the bounded command output a check stored under
// Data["output"] (set by `command` checks and app probes on failure), for
// threading into an event's Output field. Empty when absent.
func resultOutput(r checks.Result) string {
	if r.Data == nil {
		return ""
	}
	if s, ok := r.Data[checks.DataKeyOutput].(string); ok {
		return s
	}
	return ""
}

// operationEventEmitter adapts the daemon event log to the operation engine's
// per-operation emit hook. Web-initiated actions use this path; worker
// remediation keeps its own emit so it can attach the firing rule name.
func operationEventEmitter(emit func(Event)) func(operation.Result) {
	if emit == nil {
		return nil
	}
	return func(r operation.Result) {
		emit(Event{
			Service: r.Service,
			Kind:    eventKindForResult(r),
			Action:  r.Action,
			Status:  string(r.Status),
			Message: r.Message,
		})
	}
}

// eventKindForResult maps an operation result to the event-log kind. Successful
// operations are action; blocked ones are suppressed (guard/lock/cooldown); every
// other outcome (preflight/postflight failure, backend error, orphan processes)
// is error so the UI does not show a failed restart as green.
func eventKindForResult(r operation.Result) string {
	switch r.Status {
	case operation.ResultOK:
		return eventKindAction
	case operation.ResultBlocked:
		return eventKindSuppressed
	default:
		return eventKindError
	}
}

// SlogEmitter logs events through slog.
func SlogEmitter(logger *slog.Logger) func(Event) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(e Event) {
		attrs := []any{eventFieldService, e.Service, eventFieldKind, e.Kind}
		if e.Watch != "" {
			attrs = append(attrs, eventFieldWatch, e.Watch)
		}
		if e.App != "" {
			attrs = append(attrs, eventFieldApp, e.App)
		}
		if e.Rule != "" {
			attrs = append(attrs, eventFieldRule, e.Rule)
		}
		if e.Action != "" {
			attrs = append(attrs, eventFieldAction, e.Action)
		}
		if e.Status != "" {
			attrs = append(attrs, eventFieldStatus, e.Status)
		}
		if e.Message != "" {
			attrs = append(attrs, eventFieldMessage, e.Message)
		}
		switch e.Kind {
		case eventKindError, eventKindHookFail, eventKindNotifyFail, eventKindExpandFailed, eventKindKillFailed:
			logger.Error("sermod", attrs...)
		case eventKindAction, eventKindAlert, eventKindSuppressed, eventKindFiring, eventKindRecovered, eventKindDryRun, eventKindHook, eventKindNotify, eventKindCascade,
			eventKindExpand, eventKindExpandSkipped, eventKindKill, eventKindReload, eventKindPanicSuppressed, eventKindNotifySuppressed:
			logger.Info("sermod", attrs...)
		default:
			logger.Debug("sermod", attrs...)
		}
	}
}

// emitSafe forwards e to emit when an emitter is wired, and is a no-op
// otherwise.
func emitSafe(emit func(Event), e Event) {
	if emit != nil {
		emit(e)
	}
}

// reportCallbackError forwards a non-nil err to cb when a callback is wired,
// and is a no-op otherwise.
func reportCallbackError(cb func(error), err error) {
	if err != nil && cb != nil {
		cb(err)
	}
}
