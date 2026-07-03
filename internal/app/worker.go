package app

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
)

// Worker monitors one service. A cycle runs the service's checks, evaluates its
// rules (guards gate remediation), and runs at most one remediation action
// through the shared operation engine when policy allows.
type Worker struct {
	Service   string
	Rules     []rules.Rule
	Policy    rules.Policy
	State     *rules.RemediationState
	CheckDeps checks.Deps

	// Interval overrides how often this worker runs a cycle. <=0 means the
	// scheduler's global interval (engine.interval). A per-service `interval`
	// lets cheap checks run often and expensive ones run rarely.
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

	// cycleFailOutput is this cycle's concatenated failing-check command output,
	// attached to alert events so the operator sees why the rule fired.
	cycleFailOutput string

	// Checks produces this cycle's named-check cache.
	Checks func(ctx context.Context, deps checks.Deps) map[string]checks.Result
	// ResolveRefs returns a per-cycle resolver for named checks outside the main
	// monitoring cache, currently preflight entries referenced from rules.
	ResolveRefs func() rules.RefResolver
	// Sample produces this cycle's metric reader. Nil when the
	// service uses no metrics.
	Sample func(ctx context.Context) checks.MetricReader
	// LiveSample samples this cycle's live CPU readings (per-process and
	// aggregate) into the LiveMetrics registry for the web UI. Nil when no live
	// metrics registry is wired.
	LiveSample func(ctx context.Context)
	// Operate runs an action through the operation engine.
	Operate func(ctx context.Context, action string) operation.Result
	// Cascade, when set (the service declares also_apply), runs the action across
	// this service plus its additional services in dependency order, and returns
	// this service's own (primary) Result. nil → Operate is used directly.
	Cascade func(ctx context.Context, action string) operation.Result
	// IsPaused reports whether monitoring is paused for this service (operator ran
	// `unmonitor`). A paused cycle still advances cycle but runs no checks, rules
	// or remediation.
	IsPaused func() bool

	// InPanic reports whether the daemon-wide panic mode is on. Unlike IsPaused,
	// checks and rule evaluation still run (so status stays visible), but alert
	// notifications and automatic remediation actions are suppressed.
	InPanic func() bool

	// Settling tracks startup observation for this service. While unsettled the
	// worker waits for an active backend, runs one observe-only check cycle, and
	// suppresses alerts and remediation.
	Settling *Settling
	// OperationSettling tracks manual/automatic service operations in progress or
	// awaiting one post-operation observation cycle. While active, checks may
	// publish fresh data but must not drive SLA, alerts or remediation.
	OperationSettling OperationSettlingStore

	// Shadow when true causes the worker to fully evaluate remediation rules,
	// advance their for/within windows, consult guards and the remediation policy,
	// and emit events, but never perform any Operate/Cascade action and never
	// Record against the real cooldown/backoff state. This lets operators observe
	// what Sermo *would* do before enabling live auto-remediation.
	// Shadow is independent of IsPaused (paused services skip evaluation entirely).
	Shadow bool
	// RecordHealth persists this cycle's availability sample for SLA tracking:
	// up is true when no required check failed. Nil disables recording (tests, or
	// when no store is wired). Only observed (non-paused) cycles are recorded.
	RecordHealth func(up bool)
	// RecordChecks persists per-check availability for checks that actually ran
	// and were not converted to skipped by gates. Nil disables recording.
	RecordChecks func(cache map[string]checks.Result, ran map[string]bool)
	// Publish records this cycle's check cache for the web detail view. ran lists
	// checks that actually executed (cycleRan). Nil disables publishing.
	Publish func(cache map[string]checks.Result, ran map[string]bool)
	// PersistState stores remediation policy state and rule-window progress after
	// an observed cycle. Nil keeps state in memory for this process only.
	PersistState func(*rules.RemediationState, map[string]*rules.WindowState)
	Now          func() time.Time
	Emit         func(Event)

	// Notifiers are the configured delivery targets, addressable by name from a
	// rule's `notify`. Optional: nil means a rule alert only emits an event.
	Notifiers map[string]notify.Notifier
	// GlobalNotify is the top-level `notify` default a rule alert inherits when it
	// declares no `notify` of its own (see config.NotifyDefault).
	GlobalNotify []string

	// Remediation publishes the policy gating view for the web detail. Optional.
	Remediation *RemediationRegistry
	// RuleWindows publishes rule window progress for the web detail. Optional.
	RuleWindows *RuleWindowRegistry

	// MetricChecks holds checks and preflight entries for defense-in-depth
	// suppression of remediation rules that read scope: system metrics.
	MetricChecks map[string]any

	// windows holds per-rule for/within state across cycles.
	windows map[string]*rules.WindowState
	// libBaseline holds the acknowledged fingerprint of each watched path (a
	// `changed:` condition target, typically a library .so) across cycles.
	libBaseline map[string]string

	// appVersionCmd holds the resolved version command of each app the service
	// declares (keyed by app name), so a `changed: {app}` condition can sample the
	// app's current version. Built once from the resolved tree's preflight.
	appVersionCmd map[string]appVersionCmd
	// appVersions holds the acknowledged version-short of each watched app+level
	// (key "app:level") across cycles, the version analogue of libBaseline.
	appVersions map[string]string
	// appVersionsLast holds the most recently sampled version-short per app+level,
	// so acknowledgeChanges can adopt the post-restart version without re-running
	// the command.
	appVersionsLast map[string]string
}

// appVersionCmd is a resolved app version probe: the command argv (variables
// already expanded), an optional user to run it as, and an optional timeout.
type appVersionCmd struct {
	argv    []string
	user    string
	timeout time.Duration
}

// RunCycle runs one monitoring cycle for the service: build the
// check cache, evaluate remediation rules in name order, and run the first
// firing rule whose action is not guard-blocked and is allowed by policy. Then
// fire any alert rules. The internal operation lock already
// prevents overlapping operations, so cycles never run concurrently per service.
func (w *Worker) RunCycle(ctx context.Context) {
	w.cycle++
	defer w.publishRemediation()
	settleKey := SettlingServiceKey(w.Service)
	if w.IsPaused != nil && w.IsPaused() {
		if w.Settling != nil && !w.Settling.Observed(settleKey) {
			w.Settling.MarkObserved(settleKey)
		}
		return // monitoring paused for this service
	}

	now := w.Now
	if now == nil {
		now = time.Now
	}

	startupObserveOnly := w.Settling != nil && !w.Settling.Observed(settleKey)
	operationObserveOnly, operationRunning := w.operationSettlingState(now())
	if operationRunning {
		if startupObserveOnly && w.Settling != nil {
			w.Settling.MarkObserved(settleKey)
		}
		return
	}
	observeOnly := startupObserveOnly || operationObserveOnly
	if observeOnly && !w.backendActive(ctx) {
		// The init backend is inactive: complete startup observation without
		// running checks so stopped services do not block daemon readiness or
		// stay in state "starting" forever. The web/CLI surface the inactive
		// backend as failed once observed.
		if startupObserveOnly && w.Settling != nil {
			w.Settling.MarkObserved(settleKey)
		}
		return
	}

	deps := w.CheckDeps
	if w.LiveSample != nil {
		w.LiveSample(ctx) // live CPU gauge for the web; runs every monitored cycle
	}
	if w.Sample != nil {
		deps.Metrics = w.Sample(ctx)
	}
	cache := w.Checks(ctx, deps)
	w.applyGates(cache)
	w.cycleFailOutput = failingChecksOutput(cache)
	if !observeOnly {
		if w.RecordChecks != nil {
			w.RecordChecks(cache, w.cycleRan)
		}
		if w.RecordHealth != nil {
			w.RecordHealth(requiredChecksOK(cache))
		}
	}
	if w.Publish != nil {
		w.Publish(cache, w.cycleRan)
	}
	if observeOnly {
		if startupObserveOnly && w.Settling != nil {
			w.Settling.MarkObserved(settleKey)
		}
		if operationObserveOnly {
			w.clearOperationSettling()
		}
		return // first active cycle: publish data only, no rules or SLA side effects
	}
	var resolveRef rules.RefResolver
	if w.ResolveRefs != nil {
		resolveRef = w.ResolveRefs()
	}
	ev := &rules.Evaluator{Cache: cache, ResolveRef: resolveRef, Deps: deps, Changed: w.changed, ChangedVersion: w.changedAppVersion}
	evals := w.ruleEvalCache()

	at := now()
	w.runRemediation(ctx, ev, now, at, evals)
	w.runAlerts(ctx, ev, at, evals)
	w.publishRuleWindows(ctx, ev, at, evals)
	w.persistRuleState()
}

func (w *Worker) operationSettlingState(now time.Time) (observeOnly, running bool) {
	if w.OperationSettling == nil {
		return false, false
	}
	rec, found, err := w.OperationSettling.OperationSettling(w.Service)
	if err != nil {
		w.emit(Event{Kind: "error", Message: "operation settling: " + err.Error()})
		return false, false
	}
	if !found {
		return false, false
	}
	if !rec.UpdatedAt.IsZero() && now.Sub(rec.UpdatedAt) > operationSettlingMaxAge {
		w.clearOperationSettling()
		return false, false
	}
	switch rec.Phase {
	case state.OperationSettlingRunning:
		return false, true
	case state.OperationSettlingSettling:
		return true, false
	default:
		w.emit(Event{Kind: "error", Message: fmt.Sprintf("operation settling: unknown phase %q", rec.Phase)})
		w.clearOperationSettling()
		return false, false
	}
}

func (w *Worker) clearOperationSettling() {
	if w.OperationSettling == nil {
		return
	}
	if err := w.OperationSettling.ClearOperationSettling(w.Service); err != nil {
		w.emit(Event{Kind: "error", Message: "operation settling: " + err.Error()})
	}
}

func (w *Worker) backendActive(ctx context.Context) bool {
	if w.CheckDeps.Status == nil {
		return true
	}
	st, err := w.CheckDeps.Status(ctx)
	if err != nil {
		return false
	}
	return st == servicemgr.StatusActive
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
		if !r.Optional && !r.Healthy() {
			return false
		}
	}
	return true
}

// failingChecksOutput concatenates the bounded command output of this cycle's
// failing checks (those that captured stdout/stderr under Data["output"]),
// labelled by check name and ordered for stability, for attaching to alert
// events. Returns "" when no failing check captured output.
func failingChecksOutput(cache map[string]checks.Result) string {
	names := make([]string, 0, len(cache))
	for name, r := range cache {
		if r.Optional || r.Skipped || r.Healthy() {
			continue
		}
		if resultOutput(r) != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	var b strings.Builder
	for i, name := range names {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("check " + name + ":\n")
		b.WriteString(resultOutput(cache[name]))
	}
	return b.String()
}

type ruleEvalResult struct {
	cond bool
	err  error
}

func (w *Worker) ruleEvalCache() map[string]ruleEvalResult {
	if w.RuleWindows == nil || len(w.Rules) == 0 {
		return nil
	}
	return make(map[string]ruleEvalResult, len(w.Rules))
}

func (w *Worker) runRemediation(ctx context.Context, ev *rules.Evaluator, now func() time.Time, at time.Time, evals map[string]ruleEvalResult) {
	if w.State == nil {
		w.State = &rules.RemediationState{}
	}
	// Update every remediation rule's window this cycle, then act on the first
	// firing rule that is not guard-blocked. Updating all windows
	// keeps consecutive/sliding counts correct even when an earlier rule acts.
	var firing []rules.Rule
	for _, r := range w.Rules {
		if r.Type != rules.RuleRemediation {
			continue
		}
		if w.fires(ctx, ev, r, at, evals) {
			firing = append(firing, r)
		}
	}

	// A healthy cycle (no remediation rule fired) decays the backoff.
	if len(firing) == 0 {
		w.State.Recover()
		return
	}

	for _, r := range firing {
		op, hasOp := r.OperationAction()
		if !hasOp {
			// Validation rejects operation-less remediation rules; tolerate one
			// that bypassed it (hand-built Rule) by at least delivering its
			// alerts instead of silently doing nothing.
			w.emitAlerts(ctx, r)
			continue
		}
		action := string(op)
		// A remediation rule must never bypass guards. If a guard
		// blocks this action, try the next firing rule (first non-blocked wins).
		blocked, reason, err := rules.Guard(ctx, w.Rules, action, ev)
		if err != nil {
			w.emit(Event{Kind: "error", Rule: r.Name, Action: action, Message: "guard: " + err.Error()})
			continue
		}

		suppress := ""
		if blocked {
			suppress = "guard: " + reason
		} else if ok, why := w.Policy.Allow(w.State, now()); !ok {
			suppress = why
		}

		if w.Shadow {
			// Full evaluation + window advance + guard + policy happened.
			// Emit rich "shadow" event so the operator sees exactly what Sermo
			// would have done (and why it would have been suppressed, if at all).
			// Never execute the action and never Record (real cooldowns unaffected).
			msg := "would " + action
			if suppress != "" {
				msg += " (suppressed: " + suppress + ")"
			} else {
				msg += " (would execute)"
			}
			w.emit(Event{Kind: "shadow", Rule: r.Name, Action: action, Message: msg})
			// Report all firing rules in shadow mode for maximum observability.
			continue
		}

		if suppress != "" {
			w.emit(Event{Kind: "suppressed", Rule: r.Name, Action: action, Message: suppress})
			continue
		}

		// All actions of this rule run together: alerts notify, then the operation.
		w.emitAlerts(ctx, r)
		// Panic mode keeps the alert visible (emitAlerts above) but never performs
		// the automatic action; the operator drives services manually instead.
		if w.InPanic != nil && w.InPanic() {
			w.emit(Event{Kind: "suppressed", Rule: r.Name, Action: action, Message: "panic mode: remediation suppressed"})
			continue
		}
		// Cascade owns the primary's placement when also_apply is set (so stop can
		// take additionals down first); it returns this service's own Result, which
		// drives the bookkeeping below exactly as a bare Operate would.
		operate := w.operateForRemediation
		if w.Cascade != nil && (action == "start" || action == "stop" || action == "restart") {
			operate = w.Cascade
		}
		result := operate(ctx, action)
		if result.RecordsRemediation() {
			w.State.Record(now(), w.Policy)
		}
		// A successful (re)launch, reload or resume now runs against the current files,
		// so refresh the watched baselines — otherwise a `changed:`-driven
		// restart/reload would fire again every cycle.
		if result.OK() && (action == "restart" || action == "start" || action == "reload" || action == "resume") {
			w.acknowledgeChanges()
		}
		w.emit(Event{Kind: eventKindForResult(result), Rule: r.Name, Action: action, Status: string(result.Status), Message: result.Message})
		return // at most one remediation action per cycle
	}
}

func (w *Worker) operateForRemediation(ctx context.Context, action string) operation.Result {
	if err := beginOperationSettling(w.OperationSettling, w.Service, action, state.SourceDaemon); err != nil {
		w.emit(Event{Kind: "error", Action: action, Message: err.Error()})
	}
	result := w.Operate(ctx, action)
	if err := finishOperationSettling(w.OperationSettling, w.Service, action, state.SourceDaemon, result, nil); err != nil {
		w.emit(Event{Kind: "error", Action: action, Message: err.Error()})
	}
	return result
}

func (w *Worker) runAlerts(ctx context.Context, ev *rules.Evaluator, at time.Time, evals map[string]ruleEvalResult) {
	for _, r := range w.Rules {
		if r.Type != rules.RuleAlert {
			continue
		}
		if w.fires(ctx, ev, r, at, evals) {
			w.emitAlerts(ctx, r)
		}
	}
}

// emitAlerts emits each of a rule's alert messages as an `alert` event and, when
// the rule resolves to one or more notifiers (its own `notify`, or the global
// default it inherits, unless suppressed with `none`), delivers each message to
// them best-effort.
func (w *Worker) emitAlerts(ctx context.Context, r rules.Rule) {
	notifiers := resolveNotifiers(effectiveNotify(r.Notify, w.GlobalNotify), w.Notifiers)
	panicking := w.InPanic != nil && w.InPanic()
	output := w.cycleFailOutput
	for _, msg := range r.AlertMessages() {
		// The alert event is always emitted so the condition stays visible; panic
		// mode only suppresses the outbound notifications. Output carries the failing
		// command's stdout/stderr so the operator can see why the rule fired.
		w.emit(Event{Kind: "alert", Rule: r.Name, Message: msg, Output: output})
		if panicking {
			w.emit(Event{Kind: "notify-suppressed", Rule: r.Name, Message: "panic mode: alert notification suppressed"})
			continue
		}
		for _, n := range notifiers {
			if err := n.Send(ctx, alertMessage(w.Service, r.Name, msg, output)); err != nil {
				w.emit(Event{Kind: "notify-failed", Rule: r.Name, Message: n.Name() + ": " + err.Error()})
			} else {
				w.emit(Event{Kind: "notify", Rule: r.Name, Message: "notified " + n.Name()})
			}
		}
	}
}

// alertMessage builds the notification for a rule's alert message.
func alertMessage(service, rule, msg, output string) notify.Message {
	body := msg
	if output != "" {
		body += "\n\n" + output
	}
	return notify.Message{
		Subject: fmt.Sprintf("[sermo] %s: %s", service, msg),
		Body:    body,
		Fields:  map[string]string{"SERMO_SERVICE": service, "SERMO_RULE": rule},
	}
}

// fires evaluates a rule's condition this cycle and advances its window state,
// returning whether the rule fires now. An evaluation error counts
// as a false cycle.
func (w *Worker) fires(ctx context.Context, ev *rules.Evaluator, r rules.Rule, at time.Time, evals map[string]ruleEvalResult) bool {
	// Defense-in-depth for safety invariant 13: a system-scoped metric must
	// never trigger anything but an alert. ParseRules already drops such
	// rules; this catches one that bypassed parsing entirely.
	if r.Type != rules.RuleAlert && rules.ConditionUsesSystemMetric(r.If, w.MetricChecks) {
		w.emit(Event{Kind: "error", Rule: r.Name, Message: "scope: system metric may only drive alert rules; rule suppressed"})
		return false
	}
	cond, err := w.evalRule(ctx, ev, r, evals)
	if err != nil {
		w.emit(Event{Kind: "error", Rule: r.Name, Message: "evaluate: " + err.Error()})
		cond = false
	}
	return w.windowState(r.Name).FiresAt(r, cond, at)
}

func (w *Worker) evalRule(ctx context.Context, ev *rules.Evaluator, r rules.Rule, evals map[string]ruleEvalResult) (bool, error) {
	if evals != nil {
		if res, ok := evals[r.Name]; ok {
			return res.cond, res.err
		}
	}
	cond, err := ev.Eval(ctx, r.If)
	if evals != nil {
		evals[r.Name] = ruleEvalResult{cond: cond, err: err}
	}
	return cond, err
}

// LibChangedFunc returns a `changed:` evaluator backed by baseline. The worker
// and operation engine share the same map so manual actions honor the same
// acknowledged fingerprints as automatic remediation.
func LibChangedFunc(baseline map[string]string) func(string) (bool, error) {
	if baseline == nil {
		return nil
	}
	return func(path string) (bool, error) {
		return libPathChanged(baseline, path)
	}
}

func libPathChanged(baseline map[string]string, path string) (bool, error) {
	cur := fileFingerprint(path)
	base, seen := baseline[path]
	if !seen {
		baseline[path] = cur
		return false, nil
	}
	return cur != base, nil
}

// changed reports whether the file at path differs from the acknowledged
// baseline. The first observation adopts the current fingerprint (so a daemon
// start never triggers a restart); thereafter it is true until acknowledged.
func (w *Worker) changed(path string) (bool, error) {
	if w.libBaseline == nil {
		w.libBaseline = map[string]string{}
	}
	return libPathChanged(w.libBaseline, path)
}

// acknowledgeChanges refreshes every watched baseline to the current fingerprint,
// clearing pending `changed:` signals after a successful (re)launch.
func (w *Worker) acknowledgeChanges() {
	for path := range w.libBaseline {
		w.libBaseline[path] = fileFingerprint(path)
	}
	// Adopt the version sampled during this cycle's rule evaluation as the new
	// baseline. After a successful restart the service runs the upgraded app, so
	// the last-seen version is the one to acknowledge; this clears the pending
	// `changed: {app}` signal without re-running the version command.
	for key, last := range w.appVersionsLast {
		w.appVersions[key] = last
	}
}

// changedAppVersion reports whether the named app's version differs from the
// acknowledged baseline, reduced to version_short truncated at level (1=major,
// 2=minor, 3=patch). The first observation adopts the current version (so a
// daemon start never triggers a restart); thereafter it stays true until
// acknowledged. When the output carries no parseable version it compares the
// first non-empty line, so a change is never silently missed.
func (w *Worker) changedAppVersion(ctx context.Context, app string, level int) (bool, error) {
	vc, ok := w.appVersionCmd[app]
	if !ok || len(vc.argv) == 0 {
		return false, fmt.Errorf("changed condition app %q: service declares no version command for it", app)
	}
	raw, err := w.sampleVersion(ctx, vc)
	if err != nil {
		return false, err
	}
	key := checks.TruncateVersion(checks.ShortVersion(raw), level)
	if key == "" {
		key = checks.FirstNonEmptyLine(raw)
	}
	if w.appVersions == nil {
		w.appVersions = map[string]string{}
	}
	if w.appVersionsLast == nil {
		w.appVersionsLast = map[string]string{}
	}
	bkey := app + ":" + strconv.Itoa(level)
	w.appVersionsLast[bkey] = key
	base, seen := w.appVersions[bkey]
	if !seen {
		w.appVersions[bkey] = key
		return false, nil
	}
	return key != base, nil
}

// sampleVersion runs an app's version command and returns its trimmed stdout.
func (w *Worker) sampleVersion(ctx context.Context, vc appVersionCmd) (string, error) {
	runner := w.CheckDeps.Runner
	if runner == nil {
		return "", fmt.Errorf("no command runner configured")
	}
	timeout := vc.timeout
	if timeout <= 0 {
		timeout = w.CheckDeps.DefaultTimeout
	}
	var (
		res execx.Result
		err error
	)
	if vc.user != "" {
		res, err = execx.RunUser(ctx, runner, timeout, vc.user, vc.argv[0], vc.argv[1:]...)
	} else {
		res, err = execx.Run(ctx, runner, timeout, vc.argv[0], vc.argv[1:]...)
	}
	if res.ExitCode != 0 {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("version command exit %d", res.ExitCode)
	}
	return checks.TrimOutput(res.Stdout), nil
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

func (w *Worker) publishRuleWindows(ctx context.Context, ev *rules.Evaluator, at time.Time, evals map[string]ruleEvalResult) {
	if w.RuleWindows == nil || ev == nil {
		return
	}
	reports := rules.BuildRuleWindowReportsAt(ctx, w.Rules, w.windows, at, func(ctx context.Context, r rules.Rule) (bool, error) {
		return w.evalRule(ctx, ev, r, evals)
	})
	w.RuleWindows.Publish(w.Service, reports)
}

func (w *Worker) persistRuleState() {
	if w.PersistState != nil {
		w.PersistState(w.State, w.windows)
	}
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
