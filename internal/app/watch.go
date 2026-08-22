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
	// watchDryRunSharedActions counts the hook and notify entries every watch
	// type can contribute on top of its own native actions.
	watchDryRunSharedActions = 2
	watchDryRunMessagePrefix = "dry-run: would run "
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

// ClockStepper asks the local time daemon to correct the system clock now.
// Satisfied by conn.MakeStep; injected so a watch's makestep action can be
// tested without commanding a real chronyd.
type ClockStepper func(ctx context.Context, socket string) error

// MakeStepSpec is a watch's native clock-correction action (`then.makestep`):
// ask the local chronyd to step the system clock over its Unix command socket.
// It is meant for `clock` watches, it is policy-gated with a *mandatory*
// cooldown, and it acts only on an offset breach — a step is a discontinuity,
// not a slew.
type MakeStepSpec struct {
	Socket string
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
	DryRun bool
	// Severity is how grave this watch's failures are: checks.SeverityError (the
	// default) or checks.SeverityWarning, which reports the same verdict as an
	// advisory instead of an outage. It changes reporting only — the window, the
	// actions and the notifications are unchanged — so nothing that gates an
	// automatic action can read a warning as healthy.
	Severity string
	Interval time.Duration
	Now      func() time.Time
	Emit     func(Event)
	// Publish records the latest daemon-cycle check result for the web UI. It is
	// intentionally best-effort: watch actions and alerts must not depend on the
	// dashboard cache.
	Publish func(watch, checkType string, result checks.Result)
	// RecordAvailability persists one availability sample, for the watches whose
	// verdict is an availability statement (see checks.RecordsAvailability). Nil
	// disables it, and it is best-effort for the same reason Publish is: a full
	// disk must not stop a link-down alert from firing.
	RecordAvailability func(up bool, at time.Time)

	// RecordMetrics persists this watch's numeric readings, for the check types
	// that publish any. nil disables metric recording.
	RecordMetrics func(data map[string]any, at time.Time)

	// ForceSLA records this watch's verdict as an availability series even
	// though its check type is not in the availability set: the operator's
	// `sla: true`. The verdict gates below still apply — a verdictless or
	// advisory result never enters the series.
	ForceSLA bool
	// StateStore persists this watch's episode and pacing state. StateSlot
	// distinguishes multiple result streams exposed under the same watch name.
	StateStore WatchStateStore
	StateSlot  string
	// IsPaused reports whether this watch is currently paused by an operator.
	// Paused watches skip checks/hooks/notifies/expand/makestep until monitored again.
	IsPaused func() bool
	// InPanic reports whether the daemon-wide panic mode is on. A panicking watch
	// still runs its check and emits its firing event (so status stays visible)
	// but suppresses its hook, notifications and its expand/makestep action.
	InPanic func() bool
	// Settling tracks startup observation for this watch. While unsettled the
	// first cycle runs checks only and suppresses firing, hooks and notifications.
	Settling *Settling
	// Cycle, when set, replaces the default single-check/single-hook behavior.
	// Stateful multi-target watches (e.g. the file watch) use it to fire one hook
	// per detected change within a cycle, which the one-Result model cannot express.
	Cycle func(ctx context.Context)
	// RecoverHook runs once on the failed-to-ok edge — when a firing watch stops
	// firing — with the same env contract as Hook plus SERMO_EVENT=recovered.
	// It shares the DryRun and panic gates: a dry-run watch reports it, a
	// panicking one suppresses it, and it never runs while the watch is healthy.
	RecoverHook HookSpec
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

	// MakeStep, when set, asks the local time daemon to step the clock on a
	// firing cycle. It shares Policy with Expand: a watch cannot be both a
	// storage and a clock watch, so there is at most one native action.
	MakeStep *MakeStepSpec
	Stepper  ClockStepper
	Policy   rules.Policy

	state          rules.WindowState
	policyState    rules.RemediationState
	firing         bool
	lastNotifyAt   time.Time // when a notification was last dispatched this firing episode
	settled        bool      // true after the startup observation cycle completed
	stateLoaded    bool
	stateRestored  bool
	persistedState state.WatchRuntimeRecord
	unavailable    bool
}

const watchEnvAssignSeparator = "="

// cycleTarget names this watch for the scheduler panic-recovery log.
func (w *Watch) cycleTarget() string { return watchSubjectPrefix + w.Name }

// RunCycle runs the check, advances the window, and fires the hook on a firing
// cycle. An evaluation/hook error is emitted, never fatal.
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
	res := checks.Execute(ctx, w.Check)
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
	if w.updateAvailability(res) {
		if observeOnly {
			w.markSettled()
		}
		return
	}
	w.recordMetricSamples(res)
	if observeOnly {
		w.reconcileRestoredEpisode(res)
		w.markSettled()
		return
	}
	w.recordAvailabilitySample(res)
	w.dispatchRaidTransitions(ctx, res)
	w.dispatchLVMTransition(ctx, res)
	wasFiring, emitFiring, firing := w.evaluateFiring(ctx, res)
	if !firing {
		return
	}
	w.dispatchFiringActions(ctx, res, wasFiring, emitFiring)
}

// updateAvailability keeps an unavailable observation out of condition windows
// and, critically, out of automatic actions. It emits only on edges and stores
// the edge state with the rest of the watch runtime record.
func (w *Watch) updateAvailability(res checks.Result) bool {
	observation := res.Observation()
	if observation == checks.ObservationUnavailable {
		if !w.unavailable {
			w.unavailable = true
			w.emit(Event{Watch: w.Name, Kind: w.eventKind(eventKindError), Message: "check unavailable: " + res.Message})
		}
		return true
	}
	if w.unavailable {
		w.unavailable = false
		w.emit(Event{Watch: w.Name, Kind: eventKindRecovered, Message: "check available: " + res.Message})
	}
	if observation == checks.ObservationSkipped {
		return true
	}
	return false
}

// eventKind names the event this watch raises for its own bad news. An advisory
// raises the warning kind in place of both "error" and "firing": the kind is the
// one severity channel that survives a daemon restart, because it is what the
// event log stores, and it is per metric, because each metric of a net/icmp watch
// is its own Watch with its own severity. So a sleeping disk that cannot be timed
// and a link's error counter stop looking like a dead disk and a dead link.
func (w *Watch) eventKind(grave string) string {
	if w.IsWarning() {
		return eventKindWarning
	}
	return grave
}

func (w *Watch) evaluateFiring(ctx context.Context, res checks.Result) (wasFiring, emitFiring, firing bool) {
	// Actions consume the raw predicate just like rule conditions do. Observation
	// owns availability, while FireOnFail remains the explicit mapping from this
	// watch's predicate to its firing condition.
	fired := res.OK
	if w.FireOnFail {
		fired = !res.OK
	}
	if !w.state.FiresAt(w.Window, fired, w.clock()) {
		w.recover(ctx, res)
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

func (w *Watch) recover(ctx context.Context, res checks.Result) {
	if !w.firing {
		return
	}
	w.firing = false
	w.lastNotifyAt = time.Time{}
	w.emit(Event{Watch: w.Name, Kind: eventKindRecovered, Message: res.Message})
	w.runRecoverHook(ctx, res)
}

// runRecoverHook executes the recovery-edge hook, honoring the same dry-run and
// panic gates the firing-side actions honor.
func (w *Watch) runRecoverHook(ctx context.Context, res checks.Result) {
	if len(w.RecoverHook.Command) == 0 {
		return
	}
	if w.DryRun {
		w.emit(Event{Watch: w.Name, Kind: eventKindDryRun, Message: "dry-run: recover hook " + strings.Join(w.RecoverHook.Command, " ")})
		return
	}
	if w.InPanic != nil && w.InPanic() {
		w.emit(Event{Watch: w.Name, Kind: eventKindPanicSuppressed, Message: "panic mode: recover hook suppressed"})
		return
	}
	env := hookEnv(w.Name, w.CheckType, res)
	env[sermoEnvEvent] = eventKindRecovered
	if err := w.RecoverHook.Run(ctx, defaultHookRunner(w.Runner), env); err != nil {
		w.emit(Event{Watch: w.Name, Kind: eventKindHookFail, Message: "recover hook: " + err.Error()})
		return
	}
	w.emit(Event{Watch: w.Name, Kind: eventKindHook, Message: "recover hook: " + res.Message})
}

func (w *Watch) dispatchFiringActions(ctx context.Context, res checks.Result, wasFiring, emitFiring bool) {
	if emitFiring {
		w.emit(Event{Watch: w.Name, Kind: w.eventKind(eventKindFiring), Message: res.Message, Output: resultOutput(res)})
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
			w.emit(Event{Watch: w.Name, Kind: eventKindPanicSuppressed, Message: "panic mode: hook/notify/expand/makestep suppressed"})
		}
		return
	}

	if w.Expand != nil && w.Expander != nil {
		w.runExpand(ctx, res, emitFiring)
	}
	if w.MakeStep != nil && w.Stepper != nil {
		w.runMakeStep(ctx, res, emitFiring)
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
	// Result.Data is optional, so clone into a fresh map: maps.Clone(nil) is nil
	// and the transition keys below would panic writing to it.
	changed.Data = make(map[string]any, len(res.Data))
	maps.Copy(changed.Data, res.Data)
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
	// See dispatchLVMTransition: Result.Data may be nil, so build a fresh map
	// rather than cloning one that the writes below would panic on.
	result.Data = make(map[string]any, len(base.Data))
	maps.Copy(result.Data, base.Data)
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

// IsWarning reports whether this watch's failures are advisories rather than
// outages.
func (w *Watch) IsWarning() bool { return checks.IsWarning(w.Severity) }

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
	at := w.clock()
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

// runMakeStep asks the local time daemon to step the clock on a firing cycle.
// Unlike expand the cooldown is mandatory (safety invariant 8): a clock step is
// a discontinuity, so it runs at most once per cooldown window and an attempt —
// successful or not — records the time, so a failing daemon is not hammered.
func (w *Watch) runMakeStep(ctx context.Context, res checks.Result, emitSkipped bool) {
	// A clock watch fails for several reasons and only an offset breach is one a
	// step can fix. Stepping because the source is unsynchronized would jump the
	// clock by an unknown or zero correction, which is the harm this guard
	// exists to prevent.
	if code := cfgval.String(res.Data[checks.DataKeyClockFailure]); code != checks.ClockFailureOffset {
		if emitSkipped {
			w.emit(Event{Watch: w.Name, Kind: eventKindMakeStepSkipped, Message: makeStepSkipReason(code)})
		}
		return
	}
	at := w.clock()
	if allowed, reason := w.Policy.Allow(&w.policyState, at); !allowed {
		if emitSkipped {
			w.emit(Event{Watch: w.Name, Kind: eventKindMakeStepSkipped, Message: reason})
		}
		return
	}
	err := w.Stepper(ctx, w.MakeStep.Socket)
	w.policyState.Record(at, w.Policy)
	if err != nil {
		w.emit(Event{Watch: w.Name, Kind: eventKindMakeStepFailed, Message: err.Error()})
		return
	}
	w.emit(Event{Watch: w.Name, Kind: eventKindMakeStep, Message: makeStepSuccessMessage(w.MakeStep.Socket, res)})
}

// makeStepSkipReason explains a skip in the operator's terms: which failure the
// check actually reported, and why a step would not address it.
func makeStepSkipReason(code string) string {
	if code == "" {
		return "no clock sample to correct"
	}
	return "not an offset breach (" + code + ")"
}

func makeStepSuccessMessage(socket string, res checks.Result) string {
	return fmt.Sprintf("%s: stepped the system clock (%s)", socket, res.Message)
}

func expandSuccessMessage(path string, r volume.Result) string {
	return fmt.Sprintf("%s: grew %s/%s by %s", path, r.VG, r.LV, checks.HumanizeSignedBytes(r.GrewBytes))
}

// watchDryRunMessage names the actions a firing watch would run. native lists
// the watch type's own action keys (expand, makestep, kill) in the order they
// dispatch; the hook and notify tail is the same for every watch type.
func watchDryRunMessage(hook HookSpec, notifiers []notify.Notifier, native ...string) string {
	actions := make([]string, 0, len(native)+watchDryRunSharedActions)
	actions = append(actions, native...)
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
	var native []string
	if w.Expand != nil {
		native = append(native, config.WatchThenKeyExpand)
	}
	if w.MakeStep != nil {
		native = append(native, config.WatchThenKeyMakeStep)
	}
	msg := watchDryRunMessage(w.Hook, w.Notifiers, native...)
	if len(native) == 0 {
		return msg
	}
	if allowed, reason := w.Policy.Allow(&w.policyState, w.clock()); !allowed && reason != "" {
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
		Subject: watchSubject(name, message, env[sermoEnvSeverity]),
		Body:    body.String(),
		Fields:  env,
	}
}

// watchSubject renders the notification subject. An advisory says so in the
// subject line, which is the one string an operator reads in mail or chat before
// deciding whether to get up; an error keeps the unmarked form it always had.
func watchSubject(name, message, severity string) string {
	if checks.IsWarning(severity) {
		return fmt.Sprintf("[sermo][%s] %s: %s", checks.SeverityWarning, name, message)
	}
	return fmt.Sprintf("[sermo] %s: %s", name, message)
}

// hookEnv builds the SERMO_* environment for a hook. Beyond the always-present
// SERMO_WATCH/CHECK_TYPE/MESSAGE/SEVERITY, every Result.Data key is exported as
// SERMO_<UPPER_KEY> (non-alphanumerics become "_") so any check's metadata
// reaches the hook without per-type code.
func hookEnv(name, checkType string, res checks.Result) map[string]string {
	env := map[string]string{
		sermoEnvWatch:     name,
		sermoEnvCheckType: checkType,
		sermoEnvMessage:   output.Trim(res.Message),
		sermoEnvSeverity:  checks.ResolveSeverity(res.Severity, ""),
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

// recordAvailabilitySample persists one point of this watch's availability
// series, for the check types whose verdict is an availability statement.
//
// It sits after the unavailable and observe-only gates on purpose, so the three
// things that are not downtime never enter the series: a check that could not
// run at all, a startup cycle that only observed, and — through
// CountsTowardHealth — a verdictless or advisory result. A watch reporting
// `reports: state` is a sensor, and one marked `severity: warning` is an
// advisory; neither is an outage, exactly as in a service.
func (w *Watch) recordAvailabilitySample(res checks.Result) {
	if w.RecordAvailability == nil {
		return
	}
	if !w.ForceSLA && !checks.RecordsAvailability(w.CheckType, res.Data) {
		return
	}
	if !res.CountsTowardHealth() {
		return
	}
	w.RecordAvailability(res.Observation().Healthy(), w.clock())
}

// recordMetricSamples persists one point of each numeric series this watch's
// check publishes.
//
// Unlike availability it sits before the observe-only gate and applies no verdict
// test, because a measurement is not a verdict: a settling cycle measured the
// host just as truthfully as a live one, a `reports: state` sensor exists to be
// read rather than judged, and an advisory's number is the reason it is advisory.
// Only a check that could not run has nothing to record, and the caller has
// already returned for that.
func (w *Watch) recordMetricSamples(res checks.Result) {
	if w.RecordMetrics == nil || len(res.Data) == 0 {
		return
	}
	w.RecordMetrics(res.Data, w.clock())
}
