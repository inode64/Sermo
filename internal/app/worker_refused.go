package app

import (
	"context"
	"time"

	"sermo/internal/operation"
	"sermo/internal/rules"
)

type remediationRefusal struct {
	guard, reason string
	retryAt       time.Time
	attempted     bool
}

func (w *Worker) recordRefusal(rule, guard, reason string, at time.Time) {
	if w.refusedRemediations == nil {
		w.refusedRemediations = make(map[string]remediationRefusal)
	}
	w.refusedRemediations[rule] = remediationRefusal{guard: guard, reason: reason, retryAt: at.Add(w.Policy.Cooldown),
		attempted: guard == "" || w.refusedRemediations[rule].attempted}
}

func (w *Worker) refuseGuard(ctx context.Context, ev *rules.Evaluator, firing firingRule, action, guard, reason string, at time.Time) {
	previous, exists := w.refusedRemediations[firing.Name]
	if !exists || previous.guard != guard || previous.reason != reason || !at.Before(previous.retryAt) {
		w.recordRefusal(firing.Name, guard, reason, at)
		w.emit(Event{Kind: eventKindSuppressed, Severity: firing.severity, Rule: firing.Name,
			Action: action, Status: string(operation.ResultBlocked), Message: "guard: " + reason})
	}
	// Cooldown limits repeated refusals, not the alert for a graver incident.
	if firing.escalated {
		w.emitAlerts(ctx, ev, firing.Rule, true, firing.change, firing.severity)
	}
}
