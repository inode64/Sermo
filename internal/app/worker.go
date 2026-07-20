package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"sermo/internal/appinspect"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/emission"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/output"
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
	// Observability marks the service ready only after a normal observed cycle has
	// published data and recorded availability side effects.
	Observability *ObservabilityRegistry

	// DryRun causes the worker to fully evaluate automatic rules, advance their
	// for/within windows, consult guards and the remediation policy, and emit
	// events, but never perform any Operate/Cascade action and never Record
	// against the real cooldown/backoff state. Non-console notifications are also
	// suppressed; wall notifications are still delivered. DryRun is independent
	// of IsPaused (paused services skip evaluation entirely).
	DryRun bool
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
	// GlobalEmission is the top-level automatic event/notification cadence.
	GlobalEmission emission.Policy

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
	// artifactSamples provides cadence-limited catalog app/library/file observations.
	artifactSamples *ArtifactSamples

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

type workerCycleMode struct {
	observeOnly bool
	startup     bool
	operation   bool
}

// cycleTarget names this worker for the scheduler panic-recovery log.
func (w *Worker) cycleTarget() string { return "service " + w.Service }

// RunCycle runs one monitoring cycle for the service: build the
// check cache, evaluate remediation rules in name order, and run the first
// firing rule whose action is not guard-blocked and is allowed by policy. Then
// fire any alert rules. The internal operation lock already
// prevents overlapping operations, so cycles never run concurrently per service.
func (w *Worker) RunCycle(ctx context.Context) {
	w.cycle++
	defer w.publishRemediation()
	settleKey := SettlingServiceKey(w.Service)
	now := w.Now
	if now == nil {
		now = time.Now
	}
	mode, skip := w.prepareCycle(ctx, settleKey, now)
	if skip {
		return
	}

	deps, cache := w.runChecks(ctx)
	w.publishCycle(cache, mode.observeOnly)
	if mode.observeOnly {
		w.completeObserveCycle(settleKey, mode)
		return // first active cycle: publish data only, no rules or SLA side effects
	}
	at := now()
	w.markObservabilityReady(at)
	var resolveRef rules.RefResolver
	if w.ResolveRefs != nil {
		resolveRef = w.ResolveRefs()
	}
	ev := &rules.Evaluator{Cache: cache, ResolveRef: resolveRef, Deps: deps, Changed: w.changed, ChangedVersion: w.changedAppVersion}
	evals := w.ruleEvalCache()

	w.runRemediation(ctx, ev, now, at, evals)
	w.runAlerts(ctx, ev, at, evals)
	w.publishRuleWindows(ctx, ev, at, evals)
	w.persistRuleState()
}

func (w *Worker) prepareCycle(ctx context.Context, settleKey string, now func() time.Time) (workerCycleMode, bool) {
	if w.IsPaused != nil && w.IsPaused() {
		w.clearObservability()
		if w.Settling != nil && !w.Settling.Observed(settleKey) {
			w.Settling.MarkObserved(settleKey)
		}
		return workerCycleMode{}, true // monitoring paused for this service
	}

	mode := workerCycleMode{startup: w.Settling != nil && !w.Settling.Observed(settleKey)}
	var running bool
	mode.operation, running = w.operationSettlingState(now())
	if running {
		w.clearObservability()
		if mode.startup && w.Settling != nil {
			w.Settling.MarkObserved(settleKey)
		}
		return workerCycleMode{}, true
	}
	mode.observeOnly = mode.startup || mode.operation
	if !mode.observeOnly || w.backendActive(ctx) {
		return mode, false
	}

	// The init backend is inactive: complete startup observation without running
	// checks so stopped services do not block daemon readiness or stay "starting".
	if mode.startup && w.Settling != nil {
		w.Settling.MarkObserved(settleKey)
	}
	w.clearObservability()
	return workerCycleMode{}, true
}

func (w *Worker) runChecks(ctx context.Context) (checks.Deps, map[string]checks.Result) {
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
	return deps, cache
}

func (w *Worker) publishCycle(cache map[string]checks.Result, observeOnly bool) {
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
}

func (w *Worker) completeObserveCycle(settleKey string, mode workerCycleMode) {
	if mode.startup && w.Settling != nil {
		w.Settling.MarkObserved(settleKey)
	}
	if mode.operation {
		w.clearOperationSettling()
	}
	w.clearObservability()
}

func (w *Worker) operationSettlingState(now time.Time) (observeOnly, running bool) {
	if w.OperationSettling == nil {
		return false, false
	}
	rec, found, err := w.OperationSettling.OperationSettling(w.Service)
	if err != nil {
		w.emit(Event{Kind: eventKindError, Message: "operation settling: " + err.Error()})
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
		w.emit(Event{Kind: eventKindError, Message: fmt.Sprintf("operation settling: unknown phase %q", rec.Phase)})
		w.clearOperationSettling()
		return false, false
	}
}

func (w *Worker) clearOperationSettling() {
	if w.OperationSettling == nil {
		return
	}
	if err := w.OperationSettling.ClearOperationSettling(w.Service); err != nil {
		w.emit(Event{Kind: eventKindError, Message: "operation settling: " + err.Error()})
	}
}

func (w *Worker) clearObservability() {
	if w.Observability != nil {
		w.Observability.Clear(w.Service)
	}
}

func (w *Worker) markObservabilityReady(at time.Time) {
	if w.Observability != nil {
		w.Observability.MarkReady(w.Service, at)
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
	cond   bool
	err    error
	change rules.ChangeContext
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
	firing := w.firingRemediationRules(ctx, ev, at, evals)

	// A healthy cycle (no remediation rule fired) decays the backoff. Dry-run
	// cycles must not mutate the real remediation policy state.
	if len(firing) == 0 {
		if !w.DryRun {
			w.State.Recover()
		}
		return
	}

	for i := range firing {
		if w.runFiringRemediation(ctx, ev, now, firing[i]) {
			return
		}
	}
}

func (w *Worker) runFiringRemediation(ctx context.Context, ev *rules.Evaluator, now func() time.Time, firing firingRule) bool {
	op, hasOperation := firing.Rule.OperationAction()
	if !hasOperation {
		w.emitRemediationAlerts(ctx, ev, firing)
		return false
	}
	action := string(op)
	suppress, skip := w.remediationSuppression(ctx, ev, firing, action, now)
	if skip {
		return false
	}
	if w.DryRun {
		w.emitDryRunRemediation(ctx, ev, firing, action, suppress)
		return false
	}
	if suppress != "" {
		if w.shouldEmitRuleEvent(firing.Rule, firing.rising) {
			w.emit(Event{Kind: eventKindSuppressed, Rule: firing.Name, Action: action, Message: suppress})
		}
		return false
	}
	w.emitAlerts(ctx, ev, firing.Rule, true, firing.change)
	if w.InPanic != nil && w.InPanic() {
		if w.shouldEmitRuleEvent(firing.Rule, firing.rising) {
			w.emit(Event{Kind: eventKindSuppressed, Rule: firing.Name, Action: action, Message: "panic mode: remediation suppressed"})
		}
		return false
	}
	w.executeRemediation(ctx, now, firing, action)
	return true
}

func (w *Worker) emitRemediationAlerts(ctx context.Context, ev *rules.Evaluator, firing firingRule) {
	if w.DryRun {
		w.emitDryRunAlerts(ctx, ev, firing.Rule, firing.rising, firing.change)
		return
	}
	w.emitAlerts(ctx, ev, firing.Rule, firing.rising, firing.change)
}

func (w *Worker) remediationSuppression(ctx context.Context, ev *rules.Evaluator, firing firingRule, action string, now func() time.Time) (string, bool) {
	blocked, reason, err := rules.Guard(ctx, w.Rules, action, ev)
	if err != nil {
		w.emit(Event{Kind: eventKindError, Rule: firing.Name, Action: action, Message: "guard: " + err.Error()})
		return "", true
	}
	if blocked {
		return "guard: " + reason, false
	}
	if ok, reason := w.Policy.Allow(w.State, now()); !ok {
		return reason, false
	}
	return "", false
}

func (w *Worker) emitDryRunRemediation(ctx context.Context, ev *rules.Evaluator, firing firingRule, action, suppress string) {
	message := "would " + action + " (would execute)"
	if suppress != "" {
		message = "would " + action + " (suppressed: " + suppress + ")"
	}
	if w.shouldEmitRuleEvent(firing.Rule, firing.rising) {
		w.emit(Event{Kind: eventKindDryRun, Rule: firing.Name, Action: action, Message: message})
	}
	if suppress == "" {
		w.emitDryRunAlerts(ctx, ev, firing.Rule, firing.rising, firing.change)
	}
}

func (w *Worker) executeRemediation(ctx context.Context, now func() time.Time, firing firingRule, action string) {
	operate := w.operateForRemediation
	if w.Cascade != nil && (action == string(rules.ActionStart) || action == string(rules.ActionStop) || action == string(rules.ActionRestart)) {
		operate = w.Cascade
	}
	result := operate(ctx, action)
	if result.RecordsRemediation() {
		w.State.Record(now(), w.Policy)
	}
	if result.OK() && (action == string(rules.ActionRestart) || action == string(rules.ActionStart) || action == string(rules.ActionReload) || action == string(rules.ActionResume)) {
		w.acknowledgeChanges()
	}
	w.emit(Event{Kind: eventKindForResult(result), Rule: firing.Name, Action: action, Status: string(result.Status), Message: result.Message})
}

func (w *Worker) firingRemediationRules(ctx context.Context, ev *rules.Evaluator, at time.Time, evals map[string]ruleEvalResult) []firingRule {
	var firing []firingRule
	for i := range w.Rules {
		rule := &w.Rules[i]
		if rule.Type != rules.RuleRemediation {
			continue
		}
		evaluation := w.fires(ctx, ev, *rule, at, evals)
		if evaluation.firing {
			firing = append(firing, firingRule{Rule: *rule, rising: evaluation.rising, change: evaluation.change})
		}
	}
	return firing
}

func (w *Worker) operateForRemediation(ctx context.Context, action string) operation.Result {
	if err := beginOperationSettling(w.OperationSettling, w.Service, action, state.SourceDaemon); err != nil {
		w.emit(Event{Kind: eventKindError, Action: action, Message: err.Error()})
	}
	result := w.Operate(ctx, action)
	if err := finishOperationSettling(w.OperationSettling, w.Service, action, state.SourceDaemon, result, nil, false); err != nil {
		w.emit(Event{Kind: eventKindError, Action: action, Message: err.Error()})
	}
	return result
}

func (w *Worker) runAlerts(ctx context.Context, ev *rules.Evaluator, at time.Time, evals map[string]ruleEvalResult) {
	for i := range w.Rules {
		rule := &w.Rules[i]
		if rule.Type != rules.RuleAlert {
			continue
		}
		fireState := w.fires(ctx, ev, *rule, at, evals)
		if fireState.firing {
			if w.DryRun {
				w.emitDryRunAlerts(ctx, ev, *rule, fireState.rising, fireState.change)
			} else {
				w.emitAlerts(ctx, ev, *rule, fireState.rising, fireState.change)
			}
		} else if fireState.recovered && w.shouldEmitRuleEvent(*rule, true) {
			w.emit(Event{Kind: eventKindRecovered, Rule: rule.Name, Message: w.recoveredRuleMessage(ev, *rule, fireState.change)})
		}
	}
}

// emitAlerts emits each of a rule's alert messages as an `alert` event and, when
// the rule resolves to one or more notifiers (its own `notify`, or the global
// default it inherits, unless suppressed with `none`), delivers each message to
// them best-effort.
func (w *Worker) emitAlerts(ctx context.Context, ev *rules.Evaluator, r rules.Rule, rising bool, change rules.ChangeContext) {
	w.emitAlertsFiltered(ctx, ev, r, nil, rising, change)
}

func (w *Worker) emitDryRunAlerts(ctx context.Context, ev *rules.Evaluator, r rules.Rule, rising bool, change rules.ChangeContext) {
	w.emitAlertsFiltered(ctx, ev, r, dryRunConsoleNotifier, rising, change)
}

func (w *Worker) emitAlertsFiltered(ctx context.Context, ev *rules.Evaluator, r rules.Rule, allow func(notify.Notifier) bool, rising bool, change rules.ChangeContext) {
	notifiers := resolveNotifiers(effectiveNotify(r.Notify, w.GlobalNotify), w.Notifiers)
	panicking := w.InPanic != nil && w.InPanic()
	failOutput := w.cycleFailOutput
	emitEvent := w.shouldEmitRuleEvent(r, rising)
	emitNotify := w.shouldNotifyRule(r, rising)
	for _, msg := range r.AlertMessages() {
		msg = w.expandRuleRuntime(msg, ev, r, change)
		// Output carries the failing command's stdout/stderr so the operator can see
		// why the rule fired on emitted cycles.
		if emitEvent {
			w.emit(Event{Kind: eventKindAlert, Rule: r.Name, Message: msg, Output: failOutput})
		}
		if panicking {
			if emitEvent {
				w.emit(Event{Kind: eventKindNotifySuppressed, Rule: r.Name, Message: "panic mode: alert notification suppressed"})
			}
			continue
		}
		if !emitNotify {
			continue
		}
		for _, n := range notifiers {
			if allow != nil && !allow(n) {
				continue
			}
			if err := n.Send(ctx, alertMessage(w.Service, r.Name, msg, failOutput)); err != nil {
				w.emit(Event{Kind: eventKindNotifyFail, Rule: r.Name, Message: n.Name() + ": " + err.Error()})
			} else {
				w.emit(Event{Kind: eventKindNotify, Rule: r.Name, Message: "notified " + n.Name()})
			}
		}
	}
}

// alertMessage builds the notification for a rule's alert message.
func alertMessage(service, rule, msg, failOutput string) notify.Message {
	body := msg
	if failOutput != "" {
		body += "\n\n" + failOutput
	}
	return notify.Message{
		Subject: fmt.Sprintf("[sermo] %s: %s", service, msg),
		Body:    body,
		Fields:  map[string]string{sermoEnvService: service, sermoEnvRule: rule},
	}
}

type ruleFiringState struct {
	firing    bool
	rising    bool
	recovered bool
	change    rules.ChangeContext
}

type firingRule struct {
	rules.Rule
	rising bool
	change rules.ChangeContext
}

// fires evaluates a rule's condition this cycle and advances its window state.
// An evaluation error counts as a false cycle.
func (w *Worker) fires(ctx context.Context, ev *rules.Evaluator, r rules.Rule, at time.Time, evals map[string]ruleEvalResult) ruleFiringState {
	// Defense-in-depth for safety invariant 13: a system-scoped metric must
	// never trigger anything but an alert. ParseRules already drops such
	// rules; this catches one that bypassed parsing entirely.
	if r.Type != rules.RuleAlert && rules.ConditionUsesSystemMetric(r.If, w.MetricChecks) {
		w.emit(Event{Kind: eventKindError, Rule: r.Name, Message: "scope: system metric may only drive alert rules; rule suppressed"})
		return ruleFiringState{}
	}
	cond, err := w.evalRule(ctx, ev, r, evals)
	if err != nil {
		w.emit(Event{Kind: eventKindError, Rule: r.Name, Message: "evaluate: " + err.Error()})
		cond = false
	}
	window := w.windowState(r.Name)
	// The previous cycle's episode state is the only reliable edge reference:
	// recomputing IsFiringAt with this cycle's timestamp reads a for:{duration}
	// window as already elapsed, which made rising unobservable (alerts were
	// silently never emitted while recovered fired on every episode end).
	wasFiring := window.Firing()
	firing := window.FiresAt(r, cond, at)
	return ruleFiringState{firing: firing, rising: !wasFiring && firing, recovered: wasFiring && !firing, change: ev.Change}
}

func (w *Worker) ruleEmission(r rules.Rule) emission.Policy {
	return emission.Resolve(r.Emission, emission.Resolve(w.GlobalEmission, emission.Default()))
}

func (w *Worker) shouldEmitRuleEvent(r rules.Rule, rising bool) bool {
	return emission.ShouldRepeat(w.ruleEmission(r).Events, rising)
}

func (w *Worker) shouldNotifyRule(r rules.Rule, rising bool) bool {
	return emission.ShouldRepeat(w.ruleEmission(r).Notify, rising)
}

func (w *Worker) evalRule(ctx context.Context, ev *rules.Evaluator, r rules.Rule, evals map[string]ruleEvalResult) (bool, error) {
	if evals != nil {
		if res, ok := evals[r.Name]; ok {
			if ev != nil {
				ev.Change = res.change
			}
			return res.cond, res.err
		}
	}
	if ev != nil {
		ev.Change = rules.ChangeContext{}
	}
	cond, err := ev.Eval(ctx, r.If)
	change := rules.ChangeContext{}
	if ev != nil {
		change = ev.Change
	}
	if evals != nil {
		evals[r.Name] = ruleEvalResult{cond: cond, err: err, change: change}
	}
	if err != nil {
		return cond, fmt.Errorf("evaluate rule condition: %w", err)
	}
	return cond, nil
}

// ArtifactChangedFunc returns a `changed:` evaluator backed by baseline. The
// worker and operation engine share the same map so manual actions honor the
// same acknowledged fingerprints as automatic remediation.
func ArtifactChangedFunc(baseline map[string]string, samples ...*ArtifactSamples) func(string) (bool, error) {
	if baseline == nil {
		return nil
	}
	var artifactSamples *ArtifactSamples
	if len(samples) > 0 {
		artifactSamples = samples[0]
	}
	return func(path string) (bool, error) {
		return artifactPathChanged(baseline, path, artifactSamples)
	}
}

func artifactPathChanged(baseline map[string]string, path string, samples *ArtifactSamples) (bool, error) {
	return artifactPathChangedWithFingerprint(baseline, path, samples, fileFingerprint)
}

func artifactPathChangedWithFingerprint(baseline map[string]string, path string, samples *ArtifactSamples, directFingerprint func(string) string) (bool, error) {
	cur, observed := currentArtifactFingerprint(path, samples, directFingerprint)
	if !observed {
		return false, nil
	}
	base, seen := baseline[path]
	if !seen {
		baseline[path] = cur
		return false, nil
	}
	return cur != base, nil
}

// currentArtifactFingerprint returns the cache sample when the path is an
// artifact watch target. An unsampled target deliberately has no current value:
// callers must wait for the artifact cadence instead of bypassing it with a
// direct filesystem read.
func currentArtifactFingerprint(path string, samples *ArtifactSamples, directFingerprint func(string) string) (string, bool) {
	if samples != nil {
		if sample, tracked, sampled := samples.FileFingerprint(path); tracked {
			if !sampled {
				return "", false
			}
			return sample, true
		}
	}
	return directFingerprint(path), true
}

// changed reports whether the file at path differs from the acknowledged
// baseline. The first observation adopts the current fingerprint (so a daemon
// start never triggers a restart); thereafter it is true until acknowledged.
func (w *Worker) changed(path string) (bool, error) {
	if w.libBaseline == nil {
		w.libBaseline = map[string]string{}
	}
	return artifactPathChanged(w.libBaseline, path, w.artifactSamples)
}

// acknowledgeChanges refreshes every watched baseline and cache entry after a
// successful (re)launch. This one-off refresh keeps the acknowledged baseline
// aligned with the cache when an artifact changes during the operation, without
// adding filesystem work to normal service cycles.
func (w *Worker) acknowledgeChanges() {
	for path := range w.libBaseline {
		if w.artifactSamples != nil {
			if _, tracked, _ := w.artifactSamples.FileFingerprint(path); tracked {
				w.artifactSamples.StoreFile(path)
			}
		}
		if fingerprint, observed := currentArtifactFingerprint(path, w.artifactSamples, fileFingerprint); observed {
			w.libBaseline[path] = fingerprint
		}
	}
	// Adopt the version sampled during this cycle's rule evaluation as the new
	// baseline. After a successful restart the service runs the upgraded app, so
	// the last-seen version is the one to acknowledge; this clears the pending
	// `changed: {app}` signal without re-running the version command.
	maps.Copy(w.appVersions, w.appVersionsLast)
}

// changedAppVersion reports whether the named app's version differs from the
// acknowledged baseline, reduced to version_short truncated at level (1=major,
// 2=minor, 3=patch). The first observation adopts the current version (so a
// daemon start never triggers a restart); thereafter it stays true until
// acknowledged. When the output carries no parseable version it compares the
// first non-empty line, so a change is never silently missed.
func (w *Worker) changedAppVersion(ctx context.Context, app string, level int) (bool, error) {
	if w.artifactSamples != nil {
		raw, status, sampled := w.artifactSamples.AppVersion(app)
		if !sampled {
			// Artifact watches own app probes. Waiting for the first sample avoids
			// turning an absent optional binary into a service-rule error.
			return false, nil
		}
		if appinspect.IsNotInstalledStatus(status) {
			return false, nil
		}
		if status != appinspect.StatusOK {
			return false, errors.New(status)
		}
		return w.compareAppVersion(app, level, raw)
	}
	vc, ok := w.appVersionCmd[app]
	if !ok || len(vc.argv) == 0 {
		return false, fmt.Errorf("changed condition app %q: no sampled artifact or version command", app)
	}
	raw, err := w.sampleVersion(ctx, vc)
	if err != nil {
		return false, err
	}
	return w.compareAppVersion(app, level, raw)
}

func (w *Worker) compareAppVersion(app string, level int, raw string) (bool, error) {
	key := checks.TruncateVersion(checks.ShortVersion(raw), level)
	if key == "" {
		key = output.FirstNonEmptyLine(raw)
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
		return "", errors.New("no command runner configured")
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
	if res.ExitCode != execx.ExitCodeSuccess {
		if err != nil {
			return "", fmt.Errorf("version command %s: %w", vc.argv[0], err)
		}
		return "", fmt.Errorf("version command exit %d", res.ExitCode)
	}
	return output.Trim(res.Stdout), nil
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

const (
	runtimePlaceholderDate           = "${date}"
	runtimePlaceholderEvent          = "${event}"
	runtimePlaceholderAction         = "${action}"
	runtimePlaceholderService        = "${service}"
	runtimePlaceholderRuleDuration   = "${rule.duration}"
	runtimePlaceholderRuleWindow     = "${rule.window}"
	runtimePlaceholderCheckName      = "${check.name}"
	runtimePlaceholderCheckType      = "${check.type}"
	runtimePlaceholderCheckMetric    = "${check.metric}"
	runtimePlaceholderCheckScope     = "${check.scope}"
	runtimePlaceholderCheckOp        = "${check.op}"
	runtimePlaceholderCheckThreshold = "${check.threshold}"
	runtimePlaceholderCheckValue     = "${check.value}"
	runtimePlaceholderChangePath     = "${change.path}"
	runtimePlaceholderChangeApp      = "${change.app}"
	runtimePlaceholderChangeLibrary  = "${change.library}"
	runtimePlaceholderChangeLevel    = "${change.level}"
	runtimePlaceholderChangeOld      = "${change.old_version}"
	runtimePlaceholderChangeNew      = "${change.new_version}"
)

type ruleRuntimeContext struct {
	ruleDuration   string
	ruleWindow     string
	checkName      string
	checkType      string
	checkMetric    string
	checkScope     string
	checkOp        string
	checkThreshold string
	checkValue     string
	changePath     string
	changeApp      string
	changeLibrary  string
	changeLevel    string
	changeOld      string
	changeNew      string
}

type ruleCheckCandidate struct {
	ref          string
	inlineMetric map[string]any
}

// expandRuleRuntime substitutes a rule alert message's runtime built-ins for
// both the event log and notifier delivery.
func (w *Worker) expandRuleRuntime(msg string, ev *rules.Evaluator, r rules.Rule, change rules.ChangeContext) string {
	if !strings.Contains(msg, "${") {
		return msg
	}
	event := Event{Rule: r.Name, Action: string(r.Primary().Type), Service: w.Service}
	return w.expandRuntimeWithContext(msg, event, w.ruleRuntimeContext(ev, r, change))
}

// recoveredRuleMessage records the current reading for a simple check condition,
// so an operator can distinguish a genuine recovery from a noisy threshold.
func (w *Worker) recoveredRuleMessage(ev *rules.Evaluator, r rules.Rule, change rules.ChangeContext) string {
	const prefix = "rule condition recovered"
	rc := w.ruleRuntimeContext(ev, r, change)
	if rc.checkValue == "" {
		return prefix
	}

	label := strings.TrimSpace(strings.Join([]string{rc.checkType, rc.checkMetric}, " "))
	if label == "" {
		label = rc.checkName
	}
	if label == "" {
		return prefix
	}

	message := prefix + ": " + label + " current " + rc.checkValue
	if rc.checkOp != "" && rc.checkThreshold != "" {
		message += " (threshold " + rc.checkOp + " " + rc.checkThreshold + ")"
	}
	return message
}

// expandRuntime substitutes the runtime built-ins a message may carry: event
// fields such as ${date}/${event}/${action}/${service}, and the rule context
// supplied by expandRuleRuntime. ${host} was already substituted at resolution.
func (w *Worker) expandRuntime(msg string, e Event) string {
	return w.expandRuntimeWithContext(msg, e, ruleRuntimeContext{})
}

func (w *Worker) expandRuntimeWithContext(msg string, e Event, rc ruleRuntimeContext) string {
	if !strings.Contains(msg, "${") {
		return msg
	}
	now := w.Now
	if now == nil {
		now = time.Now
	}
	service := e.Service
	if service == "" {
		service = w.Service
	}
	r := strings.NewReplacer(
		runtimePlaceholderDate, now().Format(time.RFC3339),
		runtimePlaceholderEvent, e.Rule,
		runtimePlaceholderAction, e.Action,
		runtimePlaceholderService, service,
		runtimePlaceholderRuleDuration, rc.ruleDuration,
		runtimePlaceholderRuleWindow, rc.ruleWindow,
		runtimePlaceholderCheckName, rc.checkName,
		runtimePlaceholderCheckType, rc.checkType,
		runtimePlaceholderCheckMetric, rc.checkMetric,
		runtimePlaceholderCheckScope, rc.checkScope,
		runtimePlaceholderCheckOp, rc.checkOp,
		runtimePlaceholderCheckThreshold, rc.checkThreshold,
		runtimePlaceholderCheckValue, rc.checkValue,
		runtimePlaceholderChangePath, rc.changePath,
		runtimePlaceholderChangeApp, rc.changeApp,
		runtimePlaceholderChangeLibrary, rc.changeLibrary,
		runtimePlaceholderChangeLevel, rc.changeLevel,
		runtimePlaceholderChangeOld, rc.changeOld,
		runtimePlaceholderChangeNew, rc.changeNew,
	)
	return r.Replace(msg)
}

func (w *Worker) ruleRuntimeContext(ev *rules.Evaluator, r rules.Rule, change rules.ChangeContext) ruleRuntimeContext {
	rc := ruleRuntimeContext{
		ruleDuration: rules.WindowDurationDescription(r),
		ruleWindow:   rules.WindowDescription(r),
	}
	rc.applyChange(change, w.appVersions, w.appVersionsLast)
	candidate, ok := singleRuleCheckCandidate(r.If)
	if !ok {
		return rc
	}
	if candidate.ref != "" {
		rc.checkName = candidate.ref
		if entry, ok := w.MetricChecks[candidate.ref].(map[string]any); ok {
			rc.applyCheckEntry(entry)
		}
		if ev != nil {
			if res, ok := ev.Cache[candidate.ref]; ok {
				rc.applyCheckResult(candidate.ref, res)
			}
		}
		return rc
	}
	if candidate.inlineMetric != nil {
		rc.applyInlineMetric(ev, candidate.inlineMetric)
	}
	return rc
}

func (rc *ruleRuntimeContext) applyChange(change rules.ChangeContext, base, last map[string]string) {
	rc.changePath = change.Path
	rc.changeApp = change.App
	rc.changeLibrary = change.Library
	rc.changeLevel = change.Level
	rc.changeOld = change.OldVersion
	rc.changeNew = change.NewVersion
	if change.App == "" || change.LevelValue == 0 {
		return
	}
	bkey := change.App + ":" + strconv.Itoa(change.LevelValue)
	if rc.changeOld == "" {
		rc.changeOld = base[bkey]
	}
	if rc.changeNew == "" {
		rc.changeNew = last[bkey]
	}
}

func (rc *ruleRuntimeContext) applyCheckEntry(entry map[string]any) {
	setRuntimeField(&rc.checkType, cfgval.String(entry[checks.CheckKeyType]))
	if rc.checkType != checks.CheckTypeMetric {
		return
	}
	scope := cfgval.AsString(entry[checks.CheckKeyScope])
	if scope == "" {
		scope = checks.MetricScopeService
	}
	setRuntimeField(&rc.checkMetric, cfgval.AsString(entry[checks.CheckKeyName]))
	setRuntimeField(&rc.checkScope, scope)
	setRuntimeField(&rc.checkOp, cfgval.AsString(entry[checks.CheckKeyOp]))
	setRuntimeField(&rc.checkThreshold, cfgval.String(entry[checks.CheckKeyValue]))
}

func (rc *ruleRuntimeContext) applyCheckResult(name string, res checks.Result) {
	rc.checkName = name
	if res.Check != "" {
		rc.checkName = res.Check
	}
	if len(res.Data) == 0 {
		return
	}
	setRuntimeField(&rc.checkType, cfgval.String(res.Data[checks.DataKeyType]))
	setRuntimeField(&rc.checkMetric, cfgval.String(res.Data[checks.DataKeyMetric]))
	setRuntimeField(&rc.checkScope, cfgval.String(res.Data[checks.DataKeyScope]))
	setRuntimeField(&rc.checkOp, cfgval.String(res.Data[checks.DataKeyOp]))
	unit := cfgval.String(res.Data[checks.DataKeyUnit])
	setRuntimeField(&rc.checkThreshold, formatRuntimeCheckValue(res.Data[checks.DataKeyThreshold], unit))
	setRuntimeField(&rc.checkValue, formatRuntimeCheckValue(res.Data[checks.DataKeyValue], unit))
}

func (rc *ruleRuntimeContext) applyInlineMetric(ev *rules.Evaluator, metric map[string]any) {
	scope := cfgval.AsString(metric[rules.FieldScope])
	if scope == "" {
		scope = checks.MetricScopeService
	}
	name := cfgval.AsString(metric[rules.FieldName])
	threshold := cfgval.String(metric[rules.FieldValue])
	rc.checkName = name
	rc.checkType = checks.CheckTypeMetric
	rc.checkMetric = name
	rc.checkScope = scope
	rc.checkOp = cfgval.AsString(metric[rules.FieldOp])
	rc.checkThreshold = threshold
	if ev == nil || ev.Deps.Metrics == nil || name == "" {
		return
	}
	reading, ok := ev.Deps.Metrics(scope, name)
	if !ok {
		return
	}
	value, unit, ready, err := metrics.ReadingValueForThreshold(reading, threshold)
	if err == nil && ready {
		rc.checkThreshold = formatRuntimeCheckValue(threshold, unit)
		rc.checkValue = formatRuntimeCheckValue(value, unit)
	}
}

func setRuntimeField(field *string, value string) {
	if value != "" {
		*field = value
	}
}

func formatRuntimeCheckValue(value any, unit string) string {
	return checks.FormatDisplayValueWithUnit(checks.DataKeyValue, value, unit)
}

func singleRuleCheckCandidate(node map[string]any) (ruleCheckCandidate, bool) {
	var candidates []ruleCheckCandidate
	collectRuleCheckCandidates(node, &candidates)
	if len(candidates) != 1 {
		return ruleCheckCandidate{}, false
	}
	return candidates[0], true
}

func collectRuleCheckCandidates(node map[string]any, candidates *[]ruleCheckCandidate) {
	for op, v := range node {
		switch op {
		case rules.ConditionAnd, rules.ConditionOr:
			items, ok := v.([]any)
			if !ok {
				continue
			}
			for _, item := range items {
				if child, ok := item.(map[string]any); ok {
					collectRuleCheckCandidates(child, candidates)
				}
			}
		case rules.ConditionNot:
			if child, ok := v.(map[string]any); ok {
				collectRuleCheckCandidates(child, candidates)
			}
		case rules.ConditionFailed, rules.ConditionActive:
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if ref := cfgval.AsString(m[rules.FieldCheck]); ref != "" {
				*candidates = append(*candidates, ruleCheckCandidate{ref: ref})
				continue
			}
			if metric, ok := m[rules.ConditionMetric].(map[string]any); ok {
				*candidates = append(*candidates, ruleCheckCandidate{inlineMetric: metric})
			}
		case rules.ConditionMetric:
			if metric, ok := v.(map[string]any); ok {
				*candidates = append(*candidates, ruleCheckCandidate{inlineMetric: metric})
			}
		}
	}
}
