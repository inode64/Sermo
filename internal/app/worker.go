package app

import (
	"context"
	"fmt"
	"os"
	"sort"
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

	// Interval overrides how often this worker runs a cycle. <=0 means the
	// scheduler's global interval (engine.interval). A per-service `interval`
	// lets cheap checks run often and expensive ones run rarely (section 24).
	Interval time.Duration

	// Gates holds per-check interdependencies (by check name): a check is skipped
	// this cycle when a required check is failing or a watched file has changed.
	Gates map[string]CheckGate

	// cycle counts scheduler ticks for this worker, including paused cycles, so a
	// long `unmonitor` does not desynchronize per-check `interval` scheduling.
	cycle int

	// cycleRan lists the check names that actually ran this cycle (not reused from
	// the cache because of a per-check interval). Requires gates consult only deps
	// present here so a stale cached failure cannot skip a dependent check.
	cycleRan map[string]bool

	// Checks produces this cycle's named-check cache (section 14).
	Checks func(ctx context.Context, deps checks.Deps) map[string]checks.Result
	// Sample produces this cycle's metric reader (section 12). Nil when the
	// service uses no metrics.
	Sample func(ctx context.Context) checks.MetricReader
	// Operate runs an action through the operation engine.
	Operate func(ctx context.Context, action string) operation.Result
	// IsPaused reports whether monitoring is paused for this service (operator ran
	// `unmonitor`). A paused cycle still advances cycle but runs no checks, rules
	// or remediation.
	IsPaused func() bool
	// RecordHealth persists this cycle's availability sample for SLA tracking:
	// up is true when no required check failed. Nil disables recording (tests, or
	// when no store is wired). Only observed (non-paused) cycles are recorded.
	RecordHealth func(up bool)
	// Publish records this cycle's check cache for the web detail view. ran lists
	// checks that actually executed (cycleRan). Nil disables publishing.
	Publish func(cache map[string]checks.Result, ran map[string]bool)
	Now     func() time.Time
	Emit    func(Event)

	// Remediation publishes the policy gating view for the web detail. Optional.
	Remediation *RemediationRegistry

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
	w.cycle++
	defer w.publishRemediation()
	if w.IsPaused != nil && w.IsPaused() {
		return // monitoring paused for this service
	}

	now := w.Now
	if now == nil {
		now = time.Now
	}

	deps := w.CheckDeps
	if w.Sample != nil {
		deps.Metrics = w.Sample(ctx)
	}
	cache := w.Checks(ctx, deps)
	w.applyGates(cache)
	if w.RecordHealth != nil {
		w.RecordHealth(requiredChecksOK(cache))
	}
	if w.Publish != nil {
		w.Publish(cache, w.cycleRan)
	}
	ev := &rules.Evaluator{Cache: cache, Deps: deps, Changed: w.changed}

	w.runRemediation(ctx, ev, now)
	w.runAlerts(ctx, ev)
}

// CheckGate is one check's interdependencies: it is skipped this cycle when any
// Requires check is missing/failing, or any SkipWhenChanged path has changed since
// its acknowledged baseline (e.g. a config file or library was updated).
type CheckGate struct {
	Requires        []string
	SkipWhenChanged []string
}

// gatedChecksDue returns gated checks that were skipped in a prior cycle but are
// no longer gated off this cycle, so they should run even when their per-check
// interval would otherwise defer them.
func (w *Worker) gatedChecksDue(built []checks.Built, cache map[string]checks.Result) []checks.Built {
	if len(w.Gates) == 0 {
		return nil
	}
	byName := make(map[string]checks.Built, len(built))
	for _, b := range built {
		byName[b.Check.Name()] = b
	}
	var extra []checks.Built
	for name, gate := range w.Gates {
		if w.cycleRan != nil && w.cycleRan[name] {
			continue
		}
		r, ok := cache[name]
		if !ok || !r.Skipped {
			continue
		}
		if w.gateReason(gate, cache) != "" {
			continue
		}
		if b, ok := byName[name]; ok {
			extra = append(extra, b)
		}
	}
	sort.Slice(extra, func(i, j int) bool {
		return extra[i].Check.Name() < extra[j].Check.Name()
	})
	return extra
}

// applyGates rewrites a gated-off check's result to a Skipped result so it does
// not alert, fail the service or count toward SLA. Gates are evaluated after the
// cycle's checks ran, so Requires sees this cycle's results; SkipWhenChanged uses
// the shared change baseline (acknowledged on a successful (re)start). A skipped
// check keeps its optional flag and is marked Skipped/OK. Checks that regain
// their gate this cycle are run via gatedChecksDue before applyGates is called.
func (w *Worker) applyGates(cache map[string]checks.Result) {
	for name, gate := range w.Gates {
		r, ok := cache[name]
		if !ok || r.Skipped {
			continue
		}
		if reason := w.gateReason(gate, cache); reason != "" {
			cache[name] = checks.Result{
				Check:    name,
				OK:       true,
				Skipped:  true,
				Optional: r.Optional,
				Message:  "skipped: " + reason,
			}
		}
	}
}

// gateReason returns why a check is skipped this cycle, or "" to run it.
func (w *Worker) gateReason(gate CheckGate, cache map[string]checks.Result) string {
	for _, path := range gate.SkipWhenChanged {
		if changed, _ := w.changed(path); changed {
			return "file changed: " + path
		}
	}
	for _, dep := range gate.Requires {
		if w.cycleRan != nil && !w.cycleRan[dep] {
			continue // dependency not evaluated this cycle — do not skip
		}
		d, ok := cache[dep]
		if !ok {
			continue
		}
		if !d.OK {
			return "requires check " + dep
		}
	}
	return ""
}

// requiredChecksOK reports the service's availability this cycle: true unless a
// required (non-optional) check failed. Optional checks are warnings and do not
// affect SLA. A service with no required checks is vacuously available.
func requiredChecksOK(cache map[string]checks.Result) bool {
	for _, r := range cache {
		if !r.Optional && !r.OK {
			return false
		}
	}
	return true
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

		// Cooldown/rate limit gate the decision to act (section 16). Like guards,
		// a suppressed rule does not prevent a later firing rule from acting.
		if ok, why := w.Policy.Allow(w.State, now()); !ok {
			w.emit(Event{Kind: "suppressed", Rule: r.Name, Action: action, Message: why})
			continue
		}

		// All actions of this rule run together: alerts notify, then the operation.
		w.emitAlerts(r)
		result := w.Operate(ctx, action)
		if result.RecordsRemediation() {
			w.State.Record(now(), w.Policy)
		}
		// A successful (re)launch now runs against the current files, so refresh
		// the watched baselines — otherwise a `changed:`-driven restart would fire
		// again every cycle.
		if result.OK() && (action == "restart" || action == "start") {
			w.acknowledgeChanges()
		}
		w.emit(Event{Kind: eventKindForResult(result), Rule: r.Name, Action: action, Status: string(result.Status), Message: result.Message})
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

func (w *Worker) publishRemediation() {
	if w.Remediation == nil {
		return
	}
	now := w.Now
	if now == nil {
		now = time.Now
	}
	w.Remediation.Publish(w.Service, w.Policy, w.State, now())
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
