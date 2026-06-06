package app

import (
	"context"
	"time"

	"sermo/internal/checks"
	"sermo/internal/operation"
	"sermo/internal/rules"
)

// Worker monitors one service. A cycle runs the service's checks, evaluates its
// rules (guards gate remediation), and runs at most one remediation action
// through the shared operation engine when policy allows (section 24).
type Worker struct {
	Service   string
	Rules     []rules.Rule
	Policy    rules.Policy
	State     *rules.RemediationState
	CheckDeps checks.Deps

	// Checks produces this cycle's named-check cache (section 14).
	Checks func(ctx context.Context) map[string]checks.Result
	// Operate runs an action through the operation engine.
	Operate func(ctx context.Context, action string) operation.Result
	Now     func() time.Time
	Emit    func(Event)
}

// RunCycle runs one monitoring cycle for the service (section 24): build the
// check cache, evaluate remediation rules in name order, and run the first
// firing rule whose action is not guard-blocked and is allowed by policy. Then
// fire any alert rules. The internal operation lock (section 18) already
// prevents overlapping operations, so cycles never run concurrently per service.
func (w *Worker) RunCycle(ctx context.Context) {
	now := w.Now
	if now == nil {
		now = time.Now
	}

	cache := w.Checks(ctx)
	ev := &rules.Evaluator{Cache: cache, Deps: w.CheckDeps}

	w.runRemediation(ctx, ev, now)
	w.runAlerts(ctx, ev)
}

func (w *Worker) runRemediation(ctx context.Context, ev *rules.Evaluator, now func() time.Time) {
	for _, r := range w.Rules {
		if r.Type != rules.RuleRemediation {
			continue
		}
		fired, err := ev.Eval(ctx, r.If)
		if err != nil {
			w.emit(Event{Kind: "error", Rule: r.Name, Message: "evaluate: " + err.Error()})
			continue
		}
		if !fired {
			continue
		}

		action := string(r.Then.Type)
		// A remediation rule must never bypass guards (section 17). If a guard
		// blocks this action, try the next firing rule (first non-blocked wins).
		blocked, reason, err := rules.Guard(ctx, w.Rules, action, ev)
		if err != nil {
			w.emit(Event{Kind: "error", Rule: r.Name, Action: action, Message: "guard: " + err.Error()})
			continue
		}
		if blocked {
			w.emit(Event{Kind: "suppressed", Rule: r.Name, Action: action, Message: "guard: " + reason})
			continue
		}

		// Cooldown/rate limit gate the decision to act (section 16).
		if ok, why := w.Policy.Allow(w.State, now()); !ok {
			w.emit(Event{Kind: "suppressed", Rule: r.Name, Action: action, Message: why})
			return
		}

		result := w.Operate(ctx, action)
		w.State.Record(now(), w.Policy.MaxActionsWindow)
		w.emit(Event{Kind: "action", Rule: r.Name, Action: action, Status: string(result.Status), Message: result.Message})
		return // at most one remediation action per cycle
	}
}

func (w *Worker) runAlerts(ctx context.Context, ev *rules.Evaluator) {
	for _, r := range w.Rules {
		if r.Type != rules.RuleAlert {
			continue
		}
		fired, err := ev.Eval(ctx, r.If)
		if err != nil {
			w.emit(Event{Kind: "error", Rule: r.Name, Message: "evaluate: " + err.Error()})
			continue
		}
		if fired {
			w.emit(Event{Kind: "alert", Rule: r.Name, Message: r.Then.Message})
		}
	}
}

func (w *Worker) emit(e Event) {
	e.Service = w.Service
	if w.Emit != nil {
		w.Emit(e)
	}
}
