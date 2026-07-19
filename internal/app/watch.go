package app

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"
	"unicode"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/emission"
	"sermo/internal/notify"
	"sermo/internal/output"
	"sermo/internal/rules"
	"sermo/internal/state"
	"sermo/internal/volume"
)

const (
	watchDryRunMessageNoActions = "dry-run: no configured watch actions"
	watchDryRunMessagePrefix    = "dry-run: would run "
)

// VolumeExpander grows the filesystem backing a path. Satisfied by
// volume.Expander; injected so a watch's expand action can be tested without
// touching real LVM.
type VolumeExpander interface {
	ExpandPath(ctx context.Context, path string, by int64) (volume.Result, error)
}

// ExpandSpec is a watch's native storage-expansion action (`then.expand`): grow the
// volume backing the checked path by up to By bytes (capped to the volume
// group's free space).
type ExpandSpec struct {
	By int64
}

// Watch monitors one host resource: each cycle it runs its check, advances its
// window, and fires its hook when the condition (check.OK) holds for the window.
// It is independent of services and does not use the operation engine.
type Watch struct {
	Name string
	// App, when set, marks this as an application-monitoring watch: its events are
	// emitted on the App dimension (instead of Watch) so they are queryable and
	// shown per application, separate from host watches. Built by BuildAppWatches.
	App       string
	CheckType string // e.g. "storage"; for sermoEnvCheckType (Result.Check is the watch name)
	Check     checks.Check
	Window    rules.Rule // carries only For/Within; used by rules.WindowState.FiresAt
	Hook      HookSpec
	Runner    HookRunner
	// Notifiers receive a notification when the watch fires (the resolved
	// `then.notify` targets, or the inherited global default).
	Notifiers []notify.Notifier
	// RaidNotifyEvents filters RAID lifecycle transitions eligible for the
	// ordinary `then.notify` targets. When set, firing notifications are replaced
	// by these edge-triggered lifecycle notifications.
	RaidNotifyEvents  map[string]bool
	LVMNotifyOnChange bool
	// NotifyInterval paces re-notification while the watch stays firing. Zero
	// (the default) means notify once per firing episode, on the rising edge
	// when the alert starts. A positive value (`then.notify_interval`) re-sends
	// the notification as a reminder once that interval elapses.
	NotifyInterval time.Duration
	// Emission controls automatic firing-event and notification cadence. Empty
	// fields use the built-in on-change behavior.
	Emission emission.Policy
	// DryRun keeps watch evaluation and firing events active, but reports the
	// configured actions without executing hook, non-console notify or expand side effects.
	DryRun   bool
	Interval time.Duration
	Now      func() time.Time
	Emit     func(Event)
	// Publish records the latest daemon-cycle check result for the web UI. It is
	// intentionally best-effort: watch actions and alerts must not depend on the
	// dashboard cache.
	Publish func(watch, checkType string, result checks.Result)
	// StateStore persists this watch's episode and pacing state. StateSlot
	// distinguishes multiple result streams exposed under the same watch name.
	StateStore WatchStateStore
	StateSlot  string
	// IsPaused reports whether this watch is currently paused by an operator.
	// Paused watches skip checks/hooks/notifies/expand until monitored again.
	IsPaused func() bool
	// InPanic reports whether the daemon-wide panic mode is on. A panicking watch
	// still runs its check and emits its firing event (so status stays visible)
	// but suppresses its hook, notifications and expand action.
	InPanic func() bool
	// Settling tracks startup observation for this watch. While unsettled the
	// first cycle runs checks only and suppresses firing, hooks and notifications.
	Settling *Settling
	// Cycle, when set, replaces the default single-check/single-hook behavior.
	// Stateful multi-target watches (e.g. the file watch) use it to fire one hook
	// per detected change within a cycle, which the one-Result model cannot express.
	Cycle func(ctx context.Context)
	// FireOnFail inverts the trigger: the hook fires when the check is NOT OK,
	// instead of when it is. Health checks (tcp/http/…) are healthy at OK==true, so
	// as a watch they alert on failure; condition checks (storage/load/…) alert at
	// OK==true (threshold crossed) and leave this false.
	FireOnFail bool

	// Expand, when set, runs a native storage-expansion action on a firing cycle,
	// gated by Policy so it does not run every cycle while the volume stays low.
	// It is meant for `storage` watches; the target path comes from the check
	// Result's checks.DataKeyPath data.
	Expand   *ExpandSpec
	Expander VolumeExpander
	Policy   rules.Policy

	state          rules.WindowState
	policyState    rules.RemediationState
	firing         bool
	lastNotifyAt   time.Time // when a notification was last dispatched this firing episode
	settled        bool      // true after the startup observation cycle completed
	stateLoaded    bool
	stateRestored  bool
	persistedState state.WatchRuntimeRecord
}

const watchEnvAssignSeparator = "="

// RunCycle runs the check, advances the window, and fires the hook on a firing
// cycle. An evaluation/hook error is emitted, never fatal.
// cycleTarget names this watch for the scheduler panic-recovery log.
func (w *Watch) cycleTarget() string { return "watch " + w.Name }

func (w *Watch) RunCycle(ctx context.Context) {
	observeOnly, skip := w.prepareCycle()
	if skip {
		return
	}
	if w.Cycle != nil {
		w.runCustomCycle(ctx, observeOnly)
		return
	}
	w.loadRuntimeState()
	defer w.persistRuntimeState()
	res := w.Check.Run(ctx)
	w.publish(res)
	w.runCheckCycle(ctx, res, observeOnly)
}

func (w *Watch) prepareCycle() (observeOnly, skip bool) {
	settleKey := settlingKeyForWatch(w)
	if w.IsPaused != nil && w.IsPaused() {
		if w.Settling != nil && !w.Settling.Observed(settleKey) {
			w.markSettled()
		}
		return false, true
	}
	return w.Settling != nil && !w.Settling.Observed(settleKey), false
}

func (w *Watch) runCustomCycle(ctx context.Context, observeOnly bool) {
	w.Cycle(withObserveOnly(ctx, observeOnly))
	if observeOnly {
		w.markSettled()
	}
}

func (w *Watch) runCheckCycle(ctx context.Context, res checks.Result, observeOnly bool) {
	if observeOnly {
		w.reconcileRestoredEpisode(res)
		w.markSettled()
		return
	}
	w.dispatchRaidTransitions(ctx, res)
	w.dispatchLVMTransition(ctx, res)
	wasFiring, emitFiring, firing := w.evaluateFiring(res)
	if !firing {
		return
	}
	w.dispatchFiringActions(ctx, res, wasFiring, emitFiring)
}

func (w *Watch) evaluateFiring(res checks.Result) (wasFiring, emitFiring, firing bool) {
	fired := res.OK
	if w.FireOnFail {
		fired = !res.OK
	}
	if !w.state.FiresAt(w.Window, fired, w.clock()) {
		w.recover(res)
		return false, false, false
	}
	wasFiring = w.firing
	w.firing = true
	if !fired {
		// A clear window is holding the episode open: the condition is not met
		// this cycle, so hooks/notify/expand must not run on it.
		return wasFiring, false, false
	}
	return wasFiring, w.shouldEmitFiring(wasFiring), true
}

func (w *Watch) recover(res checks.Result) {
	if !w.firing {
		return
	}
	w.firing = false
	w.lastNotifyAt = time.Time{}
	w.emit(Event{Watch: w.Name, Kind: eventKindRecovered, Message: res.Message})
}

func (w *Watch) dispatchFiringActions(ctx context.Context, res checks.Result, wasFiring, emitFiring bool) {
	if emitFiring {
		w.emit(Event{Watch: w.Name, Kind: eventKindFiring, Message: res.Message, Output: resultOutput(res)})
	}
	env := hookEnv(w.Name, w.CheckType, res)
	if w.DryRun {
		if emitFiring {
			w.emit(Event{Watch: w.Name, Kind: eventKindDryRun, Message: w.dryRunMessage()})
		}
		if len(w.RaidNotifyEvents) == 0 && !w.LVMNotifyOnChange && w.shouldNotify(wasFiring) {
			dispatchDryRunNotify(ctx, w.Notifiers, watchMessage(w.Name, res.Message, env), w.Name, w.emit)
		}
		return
	}
	if w.InPanic != nil && w.InPanic() {
		if emitFiring {
			w.emit(Event{Watch: w.Name, Kind: eventKindPanicSuppressed, Message: "panic mode: hook/notify/expand suppressed"})
		}
		return
	}

	if w.Expand != nil && w.Expander != nil {
		w.runExpand(ctx, res, emitFiring)
	}
	w.runHook(ctx, res, env)
	if len(w.RaidNotifyEvents) == 0 && !w.LVMNotifyOnChange && w.shouldNotify(wasFiring) {
		dispatchNotify(ctx, w.Notifiers, watchMessage(w.Name, res.Message, env), w.Name, w.emit)
	}
}

func (w *Watch) runHook(ctx context.Context, res checks.Result, env map[string]string) {
	if len(w.Hook.Command) > 0 {
		runner := defaultHookRunner(w.Runner)
		if err := w.Hook.Run(ctx, runner, env); err != nil {
			w.emit(Event{Watch: w.Name, Kind: eventKindHookFail, Message: err.Error()})
		} else {
			w.emit(Event{Watch: w.Name, Kind: eventKindHook, Message: res.Message})
		}
	}
}

func (w *Watch) dispatchLVMTransition(ctx context.Context, res checks.Result) {
	if !w.LVMNotifyOnChange {
		return
	}
	transition, ok := checks.LVMTransitionFromResult(res)
	if !ok {
		return
	}
	changed := res
	changed.Data = maps.Clone(res.Data)
	changed.Data["old_state"] = transition.OldState
	changed.Data["new_state"] = transition.NewState
	changed.Data["lvm_reasons"] = transition.Reasons
	changed.Data["lvm_previous_reasons"] = transition.PreviousReasons
	changed.Message = fmt.Sprintf("lvm state %s -> %s", transition.OldState, transition.NewState)
	if w.DryRun {
		dispatchDryRunNotify(ctx, w.Notifiers, watchMessage(w.Name, changed.Message, hookEnv(w.Name, w.CheckType, changed)), w.Name, w.emit)
		return
	}
	if w.InPanic != nil && w.InPanic() {
		return
	}
	dispatchNotify(ctx, w.Notifiers, watchMessage(w.Name, changed.Message, hookEnv(w.Name, w.CheckType, changed)), w.Name, w.emit)
}

func (w *Watch) dispatchRaidTransitions(ctx context.Context, res checks.Result) {
	if len(w.RaidNotifyEvents) == 0 {
		return
	}
	arrayChanges := map[string][]checks.RaidTransition{}
	for _, transition := range checks.RaidTransitions(res) {
		if !w.RaidNotifyEvents[transition.Event] {
			continue
		}
		if transition.Event == checks.RaidNotifyOnArrayChange {
			arrayChanges[transition.Array] = append(arrayChanges[transition.Array], transition)
			continue
		}
		w.dispatchRaidTransition(ctx, res, transition)
	}
	for _, array := range sortedRaidArrays(arrayChanges) {
		w.dispatchRaidTransition(ctx, res, combineRaidArrayChanges(array, arrayChanges[array]))
	}
}

func (w *Watch) dispatchRaidTransition(ctx context.Context, res checks.Result, transition checks.RaidTransition) {
	transitionResult := raidTransitionResult(res, transition)
	env := hookEnv(w.Name, w.CheckType, transitionResult)
	if w.DryRun {
		dispatchDryRunNotify(ctx, w.Notifiers, watchMessage(w.Name, transitionResult.Message, env), w.Name, w.emit)
		return
	}
	if w.InPanic != nil && w.InPanic() {
		w.emit(Event{Watch: w.Name, Kind: eventKindPanicSuppressed, Message: "panic mode: RAID notification suppressed: " + transitionResult.Message})
		return
	}
	dispatchNotify(ctx, w.Notifiers, watchMessage(w.Name, transitionResult.Message, env), w.Name, w.emit)
}

func sortedRaidArrays(changes map[string][]checks.RaidTransition) []string {
	arrays := make([]string, 0, len(changes))
	for array := range changes {
		arrays = append(arrays, array)
	}
	sort.Strings(arrays)
	return arrays
}

func combineRaidArrayChanges(array string, changes []checks.RaidTransition) checks.RaidTransition {
	fields := make([]string, 0, len(changes))
	oldValues := make([]string, 0, len(changes))
	newValues := make([]string, 0, len(changes))
	members := make([]string, 0, len(changes))
	for _, change := range changes {
		field := change.Field
		if change.Member != "" {
			members = append(members, change.Member)
			field = change.Member + "." + field
		}
		fields = append(fields, field)
		oldValues = append(oldValues, field+"="+change.Old)
		newValues = append(newValues, field+"="+change.New)
	}
	return checks.RaidTransition{
		Event: checks.RaidNotifyOnArrayChange, Array: array,
		Member: strings.Join(members, ","), Field: strings.Join(fields, ","),
		Old: strings.Join(oldValues, "; "), New: strings.Join(newValues, "; "),
	}
}

func raidTransitionResult(base checks.Result, transition checks.RaidTransition) checks.Result {
	result := base
	result.Data = maps.Clone(base.Data)
	delete(result.Data, checks.DataKeyRaidTransitions)
	delete(result.Data, checks.DataKeyRaidMembers)
	result.Data["raid_event"] = transition.Event
	result.Data["raid_array"] = transition.Array
	result.Data["raid_member"] = transition.Member
	result.Data["raid_field"] = transition.Field
	result.Data[checks.DataKeyOld] = transition.Old
	result.Data[checks.DataKeyNew] = transition.New
	result.Data[checks.DataKeyRaidOperation] = transition.Operation
	if transition.HasProgress {
		result.Data[checks.DataKeyRaidProgressPct] = transition.Progress
	}
	result.Message = raidTransitionMessage(transition)
	return result
}

// raidSubjectPrefix names a RAID array as the subject of a transition message.
const raidSubjectPrefix = "raid "

func raidTransitionMessage(transition checks.RaidTransition) string {
	switch transition.Event {
	case checks.RaidNotifyOnDegraded:
		return raidSubjectPrefix + transition.Array + " degraded"
	case checks.RaidNotifyOnRecovering:
		return raidSubjectPrefix + transition.Array + " reconstruction started"
	case checks.RaidNotifyOnGood:
		return raidSubjectPrefix + transition.Array + " is healthy after reconstruction"
	case checks.RaidNotifyOnArrayChange:
		target := raidSubjectPrefix + transition.Array
		if transition.Member != "" {
			target += " member " + transition.Member
		}
		return fmt.Sprintf("%s %s changed: %s -> %s", target, transition.Field, transition.Old, transition.New)
	default:
		return raidSubjectPrefix + transition.Array + " changed"
	}
}

func (w *Watch) publish(res checks.Result) {
	if w.Publish != nil {
		w.Publish(w.Name, w.CheckType, res)
	}
}

type observeOnlyKey struct{}

func withObserveOnly(ctx context.Context, observe bool) context.Context {
	if observe {
		return context.WithValue(ctx, observeOnlyKey{}, true)
	}
	return ctx
}

func observeOnlyCycle(ctx context.Context) bool {
	v, _ := ctx.Value(observeOnlyKey{}).(bool)
	return v
}

func (w *Watch) markSettled() {
	w.settled = true
	if w.Settling != nil {
		w.Settling.MarkObserved(settlingKeyForWatch(w))
	}
}

// clock returns the current time, honoring an injected w.Now for tests.
func (w *Watch) clock() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// shouldNotify reports whether the watch should dispatch a notification this
// firing cycle. It notifies once on the rising edge (when the alert starts);
// if NotifyInterval is set, it re-notifies as a reminder once that interval
// elapses while the watch stays firing. lastNotifyAt is reset on recovery, so
// a fresh firing episode always notifies again.
func (w *Watch) shouldNotify(wasFiring bool) bool {
	now := w.clock()
	if w.emissionPolicy().Notify == emission.ModeEveryCycle {
		w.lastNotifyAt = now
		return true
	}
	if !wasFiring {
		w.lastNotifyAt = now
		return true
	}
	if w.NotifyInterval > 0 && now.Sub(w.lastNotifyAt) >= w.NotifyInterval {
		w.lastNotifyAt = now
		return true
	}
	return false
}

func (w *Watch) shouldEmitFiring(wasFiring bool) bool {
	return emission.ShouldRepeat(w.emissionPolicy().Events, !wasFiring)
}

func (w *Watch) emissionPolicy() emission.Policy {
	return emission.Resolve(w.Emission, emission.Default())
}

// runExpand performs the native storage-expansion action on a firing cycle, gated
// by Policy. The action is attempted at most once per cooldown window even while
// the volume stays low; an attempt (success or failure) records the time so a
// failing expansion is not retried every cycle.
func (w *Watch) runExpand(ctx context.Context, res checks.Result, emitSkipped bool) {
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	at := now()
	if allowed, reason := w.Policy.Allow(&w.policyState, at); !allowed {
		if emitSkipped {
			w.emit(Event{Watch: w.Name, Kind: eventKindExpandSkipped, Message: reason})
		}
		return
	}
	path := cfgval.String(res.Data[checks.DataKeyPath])
	r, err := w.Expander.ExpandPath(ctx, path, w.Expand.By)
	w.policyState.Record(at, w.Policy)
	if err != nil {
		w.emit(Event{Watch: w.Name, Kind: eventKindExpandFailed, Message: err.Error()})
		return
	}
	w.emit(Event{Watch: w.Name, Kind: eventKindExpand, Message: expandSuccessMessage(path, r)})
}

func expandSuccessMessage(path string, r volume.Result) string {
	return fmt.Sprintf("%s: grew %s/%s by %s", path, r.VG, r.LV, checks.HumanizeSignedBytes(r.GrewBytes))
}

func watchDryRunMessage(hook HookSpec, notifiers []notify.Notifier, expand *ExpandSpec) string {
	const watchDryRunActionCapacity = 3

	actions := make([]string, 0, watchDryRunActionCapacity)
	if expand != nil {
		actions = append(actions, eventActionExpand)
	}
	if len(hook.Command) > 0 {
		actions = append(actions, config.WatchThenKeyHook)
	}
	if len(notifiers) > 0 {
		actions = append(actions, rules.RuleFieldNotify)
	}
	if len(actions) == 0 {
		return watchDryRunMessageNoActions
	}
	return watchDryRunMessagePrefix + strings.Join(actions, displayListSeparator)
}

func (w *Watch) dryRunMessage() string {
	msg := watchDryRunMessage(w.Hook, w.Notifiers, w.Expand)
	if w.Expand == nil {
		return msg
	}
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	if allowed, reason := w.Policy.Allow(&w.policyState, now()); !allowed && reason != "" {
		return msg + " (suppressed: " + reason + ")"
	}
	return msg
}

func (w *Watch) emit(e Event) {
	// App-watches reuse the whole Watch cycle but record their events on the App
	// dimension so they are queryable and displayed per application, not mixed
	// with host watches. RunCycle/dispatchNotify build events with Watch set;
	// reroute that identity to App here in one place.
	if w.App != "" {
		e.App = w.App
		e.Watch = ""
	}
	if w.Emit != nil {
		w.Emit(e)
	}
}

// dispatchNotify delivers msg to each notifier, emitting one event per result. A
// failed delivery is reported but never aborts the cycle (other targets and the
// hook still run) — notifications are best-effort.
func dispatchNotify(ctx context.Context, notifiers []notify.Notifier, msg notify.Message, watch string, emit func(Event)) {
	dispatchNotifyFiltered(ctx, notifiers, msg, watch, emit, nil)
}

func dispatchDryRunNotify(ctx context.Context, notifiers []notify.Notifier, msg notify.Message, watch string, emit func(Event)) {
	dispatchNotifyFiltered(ctx, notifiers, msg, watch, emit, dryRunConsoleNotifier)
}

func dispatchNotifyFiltered(ctx context.Context, notifiers []notify.Notifier, msg notify.Message, watch string, emit func(Event), allow func(notify.Notifier) bool) {
	for _, n := range notifiers {
		if allow != nil && !allow(n) {
			continue
		}
		if err := n.Send(ctx, msg); err != nil {
			emit(Event{Watch: watch, Kind: eventKindNotifyFail, Message: n.Name() + ": " + err.Error()})
		} else {
			emit(Event{Watch: watch, Kind: eventKindNotify, Message: "notified " + n.Name()})
		}
	}
}

func dryRunConsoleNotifier(n notify.Notifier) bool {
	return n != nil && n.Type() == "wall"
}

// watchMessage builds a notification from a fired watch's message and hook env.
// watchFireSpec carries the shared wiring a watcher needs to dispatch one
// fire: the dry-run/panic gates, the hook, an optional extra action, and the
// notify fan-out.
type watchFireSpec struct {
	name        string
	hook        HookSpec
	runner      HookRunner
	notifiers   []notify.Notifier
	inPanic     func() bool
	dryRun      bool
	emit        func(Event)
	dryRunLabel string // rendered actions for the dry-run event
	panicLabel  string // suppression notice for panic mode
	action      func() // runs between the hook and the notify fan-out (e.g. kill)
}

// runWatchHook runs a watch hook and emits its hook/hook-failed completion
// event; the shape every watcher shares.
func runWatchHook(ctx context.Context, hook HookSpec, runner HookRunner, emit func(Event), watch, msg string, env map[string]string) {
	if err := hook.Run(ctx, defaultHookRunner(runner), env); err != nil {
		emit(Event{Watch: watch, Kind: eventKindHookFail, Message: msg + ": " + err.Error()})
		return
	}
	emit(Event{Watch: watch, Kind: eventKindHook, Message: msg})
}

// dispatchWatchFire applies the dry-run → panic → hook → action → notify tail
// every watcher fire ends with.
func dispatchWatchFire(ctx context.Context, spec watchFireSpec, msg string, env map[string]string) {
	if spec.dryRun {
		spec.emit(Event{Watch: spec.name, Kind: eventKindDryRun, Message: spec.dryRunLabel + ": " + msg})
		dispatchDryRunNotify(ctx, spec.notifiers, watchMessage(spec.name, msg, env), spec.name, spec.emit)
		return
	}
	if spec.inPanic != nil && spec.inPanic() {
		spec.emit(Event{Watch: spec.name, Kind: eventKindPanicSuppressed, Message: spec.panicLabel + ": " + msg})
		return
	}
	if len(spec.hook.Command) > 0 {
		runWatchHook(ctx, spec.hook, spec.runner, spec.emit, spec.name, msg, env)
	}
	if spec.action != nil {
		spec.action()
	}
	dispatchNotify(ctx, spec.notifiers, watchMessage(spec.name, msg, env), spec.name, spec.emit)
}

func watchMessage(name, message string, env map[string]string) notify.Message {
	var body strings.Builder
	body.WriteString(message)
	body.WriteString("\n\n")
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		body.WriteString(k + watchEnvAssignSeparator + env[k] + appLineSeparator)
	}
	return notify.Message{
		Subject: fmt.Sprintf("[sermo] %s: %s", name, message),
		Body:    body.String(),
		Fields:  env,
	}
}

// hookEnv builds the SERMO_* environment for a hook. Beyond the always-present
// SERMO_WATCH/CHECK_TYPE/MESSAGE, every Result.Data key is exported as
// SERMO_<UPPER_KEY> (non-alphanumerics become "_") so any check's metadata
// reaches the hook without per-type code.
func hookEnv(name, checkType string, res checks.Result) map[string]string {
	env := map[string]string{
		sermoEnvWatch:     name,
		sermoEnvCheckType: checkType,
		sermoEnvMessage:   output.Trim(res.Message),
	}
	for k, v := range res.Data {
		env[sermoEnvPrefix+envKey(k)] = output.Trim(cfgval.String(v))
	}
	return env
}

// envKey uppercases a Data key and replaces any non-alphanumeric rune with "_".
func envKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(unicode.ToUpper(r))
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
