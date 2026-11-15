package app

import (
	"context"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/operation"
	"sermo/internal/rules"
)

// maxGuardRefusalInterval caps how far apart an unchanged guard refusal is
// audited. A guard that keeps refusing for the same reason (host
// infrastructure, logged-in users, a dependency that must stay up) usually
// will until an operator acts, and auditing it every cooldown only buries the
// event log; once a day still shows the rule wants to act.
const maxGuardRefusalInterval = 24 * time.Hour

// configurationRecheckInterval paces the configuration check while a refusal by
// the config preflight waits: often enough that a fixed file is retried within
// about a minute instead of after the whole cooldown, rarely enough that a
// slow config test (named-checkconf -z, a JAR walk) stays off most cycles.
const configurationRecheckInterval = time.Minute

type remediationRefusal struct {
	guard, reason string
	retryAt       time.Time
	attempted     bool
	// configPreflight marks an engine refusal by the required config
	// preflight: the configuration check that repeats that command can
	// release it, as a cleared guard releases a guard refusal.
	configPreflight bool
	// repeats counts the audits of this same guard refusal after the first:
	// each one doubles the wait before the next.
	repeats int
}

// recordEngineRefusal records an operation the engine refused (a failed
// preflight, a blocked or skipped action) for its retry after the cooldown.
func (w *Worker) recordEngineRefusal(rule string, result operation.Result, at time.Time) {
	w.recordRefusalAfter(rule, "", result.Message, at, w.Policy.Cooldown, 0)
	if refusal, ok := w.refusedRemediations[rule]; ok && refusedByConfigPreflight(result) {
		refusal.configPreflight = true
		w.refusedRemediations[rule] = refusal
	}
}

func refusedByConfigPreflight(result operation.Result) bool {
	if result.Status != operation.ResultPreflightFailed {
		return false
	}
	for _, check := range result.Checks {
		if check.Check == config.ServiceMonitorKeyConfig && !check.OK && !check.Optional {
			return true
		}
	}
	return false
}

// configurationFixed reports that a config-preflight refusal no longer holds:
// the configuration check, which runs the same command, ran this cycle and
// passed. Without a fresh pass the refusal keeps its retry time.
func (w *Worker) configurationFixed(refusal remediationRefusal, cache map[string]checks.Result) bool {
	if !refusal.configPreflight || (w.cycleRan != nil && !w.cycleRan[config.ConfigurationCheckName]) {
		return false
	}
	result, ok := cache[config.ConfigurationCheckName]
	return ok && !result.Skipped && result.Healthy()
}

// configurationRecheckDue returns the configuration check to run this cycle
// beyond its own interval while a config-preflight refusal waits, at most once
// per configurationRecheckInterval.
func (w *Worker) configurationRecheckDue(built []checks.Built, at time.Time) []checks.Built {
	if w.cycleRan[config.ConfigurationCheckName] || at.Before(w.configurationRecheckAt) || !w.configRefusalWaiting() {
		return nil
	}
	for _, b := range built {
		if b.Check.Name() == config.ConfigurationCheckName {
			w.configurationRecheckAt = at.Add(configurationRecheckInterval)
			return []checks.Built{b}
		}
	}
	return nil
}

func (w *Worker) configRefusalWaiting() bool {
	for _, refusal := range w.refusedRemediations {
		if refusal.guard == "" && refusal.configPreflight {
			return true
		}
	}
	return false
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
