// Package app is the sermod daemon: a scheduler that runs one independent worker
// per enabled service, each monitoring its service and driving guarded
// remediation through the shared operation engine (section 24).
package app

import "log/slog"

// Event records what a worker cycle did, for the operator-visible log.
type Event struct {
	Service string
	Kind    string // cycle | action | suppressed | alert | error
	Rule    string
	Action  string
	Status  string
	Message string
}

// SlogEmitter logs events through slog.
func SlogEmitter(logger *slog.Logger) func(Event) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(e Event) {
		attrs := []any{"service", e.Service, "kind", e.Kind}
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
		case "error":
			logger.Error("sermod", attrs...)
		case "action", "alert", "suppressed":
			logger.Info("sermod", attrs...)
		default:
			logger.Debug("sermod", attrs...)
		}
	}
}
