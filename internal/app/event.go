// Package app is the sermod daemon: a scheduler that runs one independent worker
// per enabled service, each monitoring its service and driving guarded
// remediation through the shared operation engine.
package app

import (
	"context"
	"log/slog"
	"strings"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/severity"
	"sermo/internal/state"
)

// Event records what a worker cycle did, for the operator-visible log.
type Event struct {
	Service string
	Watch   string // set for host-watch events (instead of Service)
	App     string // set for installed-application monitoring events (instead of Service/Watch)
	Kind    string // eventKind* value describing the visible event type
	Rule    string
	Check   string // stable check identity for notification grouping
	Action  string
	Status  string
	Message string
	// Output is the bounded stdout/stderr of the failing command behind this
	// event (app probe or service `command` check), shown expandable in the UI so
	// operators can see why it failed. Empty for events without command output.
	Output string
	// Notice marks a one-shot report of a single occurrence (a service restart
	// notice, a reclaimed operation lock) that no recovery event ever closes.
	// event_notify sends each one instead of tracking it as an open incident.
	Notice bool
	// Severity grades the event: a firing or escalation at the episode's level,
	// a recovery at the level the episode closed with. Unset means the kind
	// decides (level).
	Severity severity.Level
}

// level is the event's grade: its own Severity, else Error for an alarm or an
// operational failure, else Info for routine traffic.
func (e Event) level() severity.Level {
	if e.Severity.Valid() {
		return e.Severity
	}
	switch {
	case e.Kind == eventKindFiring, e.Kind == eventKindAlert, isErrorKind(e.Kind):
		return severity.Error
	default:
		return severity.Info
	}
}

// advisoryError reports an error an advisory incident raised — an advisory
// watch whose check became unavailable, or its failed manual probe. It is a
// health incident at its grade, not an outage: it neither counts as an error
// nor opens a paced operational-error incident.
func (e Event) advisoryError() bool {
	return e.Kind == eventKindError && e.Severity.Advisory()
}

// isErrorKind reports an operational failure: an error or a failed action.
func isErrorKind(kind string) bool {
	return kind == eventKindError || strings.HasSuffix(kind, eventKindFailedSuffix)
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

	eventKindSkippedSuffix = "-skipped"

	eventKindExpand          = config.WatchThenKeyExpand
	eventKindExpandSkipped   = config.WatchThenKeyExpand + eventKindSkippedSuffix
	eventKindExpandFailed    = config.WatchThenKeyExpand + eventKindFailedSuffix
	eventKindKill            = config.WatchThenKeyKill
	eventKindKillFailed      = config.WatchThenKeyKill + eventKindFailedSuffix
	eventKindMakeStep        = config.WatchThenKeyMakeStep
	eventKindMakeStepSkipped = config.WatchThenKeyMakeStep + eventKindSkippedSuffix
	eventKindMakeStepFailed  = config.WatchThenKeyMakeStep + eventKindFailedSuffix
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
	eventActionReplicationStart  = "replication-start"
	eventActionReleaseLock       = "release-lock"
	eventActionOperationSettling = "operation-settling"
	eventActionPanicOn           = "panic-on"
	eventActionPanicOff          = "panic-off"
	eventActionReload            = string(rules.ActionReload)
	eventActionNotifierTest      = "test"
	// eventActionReapOwnStrays names sermod's own startup hygiene: the leftovers a
	// previous incarnation left in the daemon's control group.
	eventActionReapOwnStrays = config.EngineKeyReapOwnStrays
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
	// eventMessageCheckPrefix opens a check-health change message, which then
	// carries the check's name and its own diagnostic.
	eventMessageCheckPrefix = "check "
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
	// eventFieldSeverity is the graded level of an event.
	eventFieldSeverity = "severity"
)

// resultOutput extracts the bounded command output a check stored under
// Data["output"] (set by `command` checks and app probes on failure), for
// threading into an event's Output field. Empty when absent.
func resultOutput(r checks.Result) string {
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
		emit(eventFromOperationResult(r))
	}
}

// OperationEventRecord converts one completed service operation into the
// canonical persistent event shape shared by sermod, the Web UI and sermoctl.
func OperationEventRecord(r operation.Result) state.EventRecord {
	return eventRecordFromLogged(LoggedEvent{Event: eventFromOperationResult(r)})
}

func eventFromOperationResult(r operation.Result) Event {
	return Event{Service: r.Service, Kind: eventKindForResult(r), Action: r.Action, Status: string(r.Status), Message: r.Message}
}

// CascadeEventRecord converts an additional target's final cascade outcome into
// the same relationship event emitted by daemon and web operations.
func CascadeEventRecord(root string, r operation.Result) state.EventRecord {
	return eventRecordFromLogged(LoggedEvent{
		Service: r.Service,
		Kind:    eventKindCascade,
		Action:  r.Action,
		Status:  string(r.Status),
		Message: "cascade from " + root})
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
		if e.Severity.Valid() {
			attrs = append(attrs, eventFieldSeverity, e.Severity.String())
		}
		logger.Log(context.Background(), eventLogLevel(e), daemonName, attrs...)
	}
}

// eventLogLevel is the journal level of an event. Only an alarm logs at its
// grade: a firing, an alert, or an error graded by the incident behind it (an
// advisory watch whose check became unavailable) — critical has no slog level
// of its own and logs as an error. A failed action and an ungraded error are
// errors. Everything else — a recovery, a remediation that ran or was held
// back, a dry run — is routine traffic at info, whatever incident it belongs
// to, so nothing scraping level=ERROR pages on a repair that worked.
func eventLogLevel(e Event) slog.Level {
	switch {
	case strings.HasSuffix(e.Kind, eventKindFailedSuffix):
		return slog.LevelError
	case e.Kind == eventKindError && !e.Severity.Valid():
		return slog.LevelError
	case e.Kind == eventKindFiring, e.Kind == eventKindAlert, e.Kind == eventKindError:
		return severityLogLevel(e.Severity)
	default:
		return slog.LevelInfo
	}
}

// severityLogLevel maps a grade to a journal level; an ungraded alarm stays
// at info, as it always logged.
func severityLogLevel(level severity.Level) slog.Level {
	switch level {
	case severity.Critical, severity.Error:
		return slog.LevelError
	case severity.Warning:
		return slog.LevelWarn
	case severity.Debug:
		return slog.LevelDebug
	default:
		return slog.LevelInfo
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
