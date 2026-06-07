// Package app is the sermod daemon: a scheduler that runs one independent worker
// per enabled service, each monitoring its service and driving guarded
// remediation through the shared operation engine (section 24).
package app

import (
	"log/slog"

	"sermo/internal/operation"
)

// Event records what a worker cycle did, for the operator-visible log.
type Event struct {
	Service string
	Watch   string // set for host-watch events (instead of Service)
	Kind    string // cycle | action | suppressed | alert | error | hook | hook-failed | notify | notify-failed
	Rule    string
	Action  string
	Status  string
	Message string
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

// eventKindForResult maps an operation result to the event-log kind. Blocked
// operations are suppressed (same as guard/cooldown), not a completed action.
func eventKindForResult(r operation.Result) string {
	if r.Status == operation.ResultBlocked {
		return "suppressed"
	}
	return "action"
}

// SlogEmitter logs events through slog.
func SlogEmitter(logger *slog.Logger) func(Event) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(e Event) {
		attrs := []any{"service", e.Service, "kind", e.Kind}
		if e.Watch != "" {
			attrs = append(attrs, "watch", e.Watch)
		}
		if e.Rule != "" {
			attrs = append(attrs, "rule", e.Rule)
		}
		if e.Action != "" {
			attrs = append(attrs, "action", e.Action)
		}
		if e.Status != "" {
			attrs = append(attrs, "status", e.Status)
		}
		if e.Message != "" {
			attrs = append(attrs, "message", e.Message)
		}
		switch e.Kind {
		case "error", "hook-failed", "notify-failed":
			logger.Error("sermod", attrs...)
		case "action", "alert", "suppressed", "hook", "notify":
			logger.Info("sermod", attrs...)
		default:
			logger.Debug("sermod", attrs...)
		}
	}
}
