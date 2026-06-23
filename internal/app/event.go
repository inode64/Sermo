// Package app is the sermod daemon: a scheduler that runs one independent worker
// per enabled service, each monitoring its service and driving guarded
// remediation through the shared operation engine.
package app

import (
	"log/slog"

	"sermo/internal/operation"
)

// Event records what a worker cycle did, for the operator-visible log.
type Event struct {
	Service string
	Watch   string // set for host-watch events (instead of Service)
	App     string // set for installed-application monitoring events (instead of Service/Watch)
	Kind    string // cycle | action | suppressed | shadow | alert | error | firing | recovered | dry-run | hook | hook-failed | notify | notify-failed | cascade
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

// eventKindForResult maps an operation result to the event-log kind. Successful
// operations are action; blocked ones are suppressed (guard/lock/cooldown); every
// other outcome (preflight/postflight failure, backend error, orphan processes)
// is error so the UI does not show a failed restart as green.
func eventKindForResult(r operation.Result) string {
	switch r.Status {
	case operation.ResultOK:
		return "action"
	case operation.ResultBlocked:
		return "suppressed"
	default:
		return "error"
	}
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
		if e.App != "" {
			attrs = append(attrs, "app", e.App)
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
		case "action", "alert", "suppressed", "shadow", "firing", "recovered", "dry-run", "hook", "notify", "cascade":
			logger.Info("sermod", attrs...)
		default:
			logger.Debug("sermod", attrs...)
		}
	}
}
