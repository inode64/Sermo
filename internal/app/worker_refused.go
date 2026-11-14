package app

import (
	"context"
	"time"

	"sermo/internal/operation"
	"sermo/internal/rules"
)

// maxGuardRefusalInterval caps how far apart an unchanged guard refusal is
// audited. A guard that keeps refusing for the same reason (host
// infrastructure, logged-in users, a dependency that must stay up) usually
// will until an operator acts, and auditing it every cooldown only buries the
// event log; once a day still shows the rule wants to act.
const maxGuardRefusalInterval = 24 * time.Hour

type remediationRefusal struct {
	guard, reason string
	retryAt       time.Time
	attempted     bool
	// repeats counts the audits of this same guard refusal after the first:
	// each one doubles the wait before the next.
	repeats int
}

func (w *Worker) recordRefusal(rule, guard, reason string, at time.Time) {
	w.recordRefusalAfter(rule, guard, reason, at, w.Policy.Cooldown, 0)
}

func (w *Worker) recordRefusalAfter(rule, guard, reason string, at time.Time, wait time.Duration, repeats int) {
	if w.refusedRemediations == nil {
		w.refusedRemediations = make(map[string]remediationRefusal)
	}
	w.refusedRemediations[rule] = remediationRefusal{guard: guard, reason: reason, retryAt: at.Add(wait),
		attempted: guard == "" || w.refusedRemediations[rule].attempted, repeats: repeats}
}

// guardRefusalInterval is the wait before the repeats-th re-audit of an
// unchanged guard refusal: the cooldown, doubled each time, never longer than
// maxGuardRefusalInterval nor shorter than the cooldown itself.
func guardRefusalInterval(cooldown time.Duration, repeats int) time.Duration {
	ceiling := max(cooldown, maxGuardRefusalInterval)
	wait := cooldown
	for range repeats {
		if wait >= ceiling/2 {
			return ceiling
		}
		wait *= 2
	}
	return wait
}

func (w *Worker) refuseGuard(ctx context.Context, ev *rules.Evaluator, firing firingRule, action, guard, reason string, at time.Time) {
	previous, exists := w.refusedRemediations[firing.Name]
	unchanged := exists && previous.guard == guard && previous.reason == reason
	if !unchanged || !at.Before(previous.retryAt) {
		// The guard is still read every cycle, so a new reason is audited at
		// once and a cleared guard releases the action; only the repetition of
		// the same refusal is spaced out.
		repeats := 0
		if unchanged {
			repeats = previous.repeats + 1
		}
		w.recordRefusalAfter(firing.Name, guard, reason, at, guardRefusalInterval(w.Policy.Cooldown, repeats), repeats)
		w.emit(Event{Kind: eventKindSuppressed, Severity: firing.severity, Rule: firing.Name,
			Action: action, Status: string(operation.ResultBlocked), Message: "guard: " + reason})
	}
	// Cooldown limits repeated refusals, not the alert for a graver incident.
	if firing.escalated {
		w.emitAlerts(ctx, ev, firing.Rule, true, firing.change, firing.severity)
	}
}
