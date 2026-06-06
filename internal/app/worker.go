package app

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	Checks func(ctx context.Context, deps checks.Deps) map[string]checks.Result
	// Sample produces this cycle's metric reader (section 12). Nil when the
	// service uses no metrics.
	Sample func(ctx context.Context) checks.MetricReader
	// Operate runs an action through the operation engine.
	Operate func(ctx context.Context, action string) operation.Result
	Now     func() time.Time
	Emit    func(Event)

	// windows holds per-rule for/within state across cycles (section 15).
	windows map[string]*rules.WindowState
	// libBaseline holds the acknowledged fingerprint of each watched path (a
	// `changed:` condition target, typically a library .so) across cycles.
	libBaseline map[string]string
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

	deps := w.CheckDeps
	if w.Sample != nil {
		deps.Metrics = w.Sample(ctx)
	}
	cache := w.Checks(ctx, deps)
	ev := &rules.Evaluator{Cache: cache, Deps: deps, Changed: w.changed}

	w.runRemediation(ctx, ev, now)
	w.runAlerts(ctx, ev)
}

func (w *Worker) runRemediation(ctx context.Context, ev *rules.Evaluator, now func() time.Time) {
	if w.State == nil {
		w.State = &rules.RemediationState{}
	}
	// Update every remediation rule's window this cycle, then act on the first
	// firing rule that is not guard-blocked (section 13/15). Updating all windows
	// keeps consecutive/sliding counts correct even when an earlier rule acts.
	var firing []rules.Rule
	for _, r := range w.Rules {
		if r.Type != rules.RuleRemediation {
			continue
		}
		if w.fires(ctx, ev, r) {
			firing = append(firing, r)
		}
	}

	// A healthy cycle (no remediation rule fired) decays the backoff (section 16).
	if len(firing) == 0 {
		w.State.Recover()
		return
	}

	for _, r := range firing {
		op, hasOp := r.OperationAction()
		if !hasOp {
			// A remediation rule with no operation (alert-only) just notifies.
			w.emitAlerts(r)
			continue
		}
		action := string(op)
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

		// All actions of this rule run together: alerts notify, then the operation.
		w.emitAlerts(r)
		result := w.Operate(ctx, action)
		w.State.Record(now(), w.Policy)
		// A successful (re)launch now runs against the current files, so refresh
		// the watched baselines — otherwise a `changed:`-driven restart would fire
		// again every cycle.
		if result.OK() && (action == "restart" || action == "start") {
			w.acknowledgeChanges()
		}
		w.emit(Event{Kind: "action", Rule: r.Name, Action: action, Status: string(result.Status), Message: result.Message})
		return // at most one remediation action per cycle
	}
}

func (w *Worker) runAlerts(ctx context.Context, ev *rules.Evaluator) {
	for _, r := range w.Rules {
		if r.Type != rules.RuleAlert {
			continue
		}
		if w.fires(ctx, ev, r) {
			w.emitAlerts(r)
		}
	}
}

func (w *Worker) emitAlerts(r rules.Rule) {
	for _, msg := range r.AlertMessages() {
		w.emit(Event{Kind: "alert", Rule: r.Name, Message: msg})
	}
}

// fires evaluates a rule's condition this cycle and advances its window state,
// returning whether the rule fires now (section 15). An evaluation error counts
// as a false cycle.
func (w *Worker) fires(ctx context.Context, ev *rules.Evaluator, r rules.Rule) bool {
	cond, err := ev.Eval(ctx, r.If)
	if err != nil {
		w.emit(Event{Kind: "error", Rule: r.Name, Message: "evaluate: " + err.Error()})
		cond = false
	}
	return w.windowState(r.Name).Fires(r, cond)
}

// changed reports whether the file at path differs from the acknowledged
// baseline. The first observation adopts the current fingerprint (so a daemon
// start never triggers a restart); thereafter it is true until acknowledged.
func (w *Worker) changed(path string) (bool, error) {
	if w.libBaseline == nil {
		w.libBaseline = map[string]string{}
	}
	cur := fileFingerprint(path)
	base, seen := w.libBaseline[path]
	if !seen {
		w.libBaseline[path] = cur
		return false, nil
	}
	return cur != base, nil
}

// acknowledgeChanges refreshes every watched baseline to the current fingerprint,
// clearing pending `changed:` signals after a successful (re)launch.
func (w *Worker) acknowledgeChanges() {
	for path := range w.libBaseline {
		w.libBaseline[path] = fileFingerprint(path)
	}
}

// fileFingerprint summarizes a file's identity for change detection: its size and
// modification time. A missing or unreadable file yields "", distinct from any
// real file, so install/removal counts as a change.
func fileFingerprint(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

func (w *Worker) windowState(name string) *rules.WindowState {
	if w.windows == nil {
		w.windows = map[string]*rules.WindowState{}
	}
	s := w.windows[name]
	if s == nil {
		s = &rules.WindowState{}
		w.windows[name] = s
	}
	return s
}

func (w *Worker) emit(e Event) {
	e.Service = w.Service
	e.Message = w.expandRuntime(e.Message, e)
	if w.Emit != nil {
		w.Emit(e)
	}
}

// expandRuntime substitutes the runtime built-ins a rule message may carry —
// ${date} (now), ${event} (the firing rule), ${action} — which resolution left
// literal. ${host}/${service} were already substituted at resolution.
func (w *Worker) expandRuntime(msg string, e Event) string {
	if !strings.Contains(msg, "${") {
		return msg
	}
	now := w.Now
	if now == nil {
		now = time.Now
	}
	r := strings.NewReplacer(
		"${date}", now().Format(time.RFC3339),
		"${event}", e.Rule,
		"${action}", e.Action,
		"${service}", e.Service,
	)
	return r.Replace(msg)
}
