package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/severity"
	persistedstate "sermo/internal/state"
)

// pidIncidentKey identifies one sampled process. Non-zero start ticks keep a
// recycled PID from inheriting the previous process's alert edge state.
type pidIncidentKey struct {
	pid        int
	startTicks uint64
}

// pidIncidentState retains notification cadence for a current offender. A
// zero StartTicks value cannot establish a PID incarnation, so it paces
// delivery without suppressing a fresh firing event from a potentially reused
// PID.
type pidIncidentState struct {
	lastNotify time.Time
}

// pidIncidents is the per-PID-incarnation alert state a set-evaluating process
// watch keeps: which offenders already fired, when each was last notified, and
// whether the aggregate incident is open and was announced live. The process
// policy and unowned-process watches share it so their edge, reminder and
// recovery semantics cannot drift apart.
type pidIncidents struct {
	watch          string
	slot           string
	notifyInterval time.Duration
	severity       severity.Level
	stateStore     WatchStateStore
	emit           func(Event)

	activeLoaded bool
	active       bool
	// announced records that the open incident notified someone live, so the
	// aggregate recovery goes out only for an incident its notifiers heard.
	announced bool
	state     map[pidIncidentKey]pidIncidentState
}

// newPIDIncidents returns the tracker for one watch's incident slot.
func newPIDIncidents(watch, slot string, notifyInterval time.Duration, level severity.Level, store WatchStateStore, emit func(Event)) *pidIncidents {
	return &pidIncidents{watch: watch, slot: slot, notifyInterval: notifyInterval, severity: level, stateStore: store, emit: emit}
}

// loadActive hydrates the aggregate incident from the persisted record once.
func (t *pidIncidents) loadActive() {
	if t.activeLoaded || t.stateStore == nil {
		return
	}
	t.activeLoaded = true
	rec, found, err := t.stateStore.WatchRuntimeState(t.watch, t.slot)
	if err != nil {
		emitSafe(t.emit, Event{Watch: t.watch, Kind: eventKindError, Message: "load " + t.slot + " state: " + err.Error()})
		return
	}
	if found {
		t.active = rec.Firing
		t.announced = rec.Firing && rec.NotifiedSeverity != ""
	}
}

func (t *pidIncidents) persistActive() {
	if t.stateStore == nil {
		return
	}
	rec := persistedstate.WatchRuntimeRecord{Firing: t.active}
	if t.announced {
		rec.NotifiedSeverity = t.severity.Resolved().String()
	}
	if err := t.stateStore.SetWatchRuntimeState(t.watch, t.slot, rec); err != nil {
		emitSafe(t.emit, Event{Watch: t.watch, Kind: eventKindError, Message: "persist " + t.slot + " state: " + err.Error()})
	}
}

// settle updates the aggregate incident for this cycle's offender count. It
// reports a recovery (the incident was open and no offender remains) and
// whether that incident had been announced live, so the caller can emit the
// recovered event and decide whether to notify it.
func (t *pidIncidents) settle(offenders int) (recovered, announced bool) {
	t.loadActive()
	if offenders == 0 && t.active {
		announced = t.announced
		t.active, t.announced = false, false
		t.persistActive()
		return true, announced
	}
	if offenders > 0 && !t.active {
		t.active = true
		t.persistActive()
	}
	return false, false
}

// announce records that a live notification went out for the open incident.
func (t *pidIncidents) announce() {
	if t.active && !t.announced {
		t.announced = true
		t.persistActive()
	}
}

func (t *pidIncidents) shouldRemind(state pidIncidentState, now time.Time) bool {
	return t.notifyInterval > 0 && now.Sub(state.lastNotify) >= t.notifyInterval
}

// cycle walks this cycle's offenders and drives the edge logic: an offender
// seen for the first time (or one whose incarnation cannot be told) fires;
// one already fired is only reminded when notify_interval elapsed. fire's
// notifyNow says whether this firing also notifies; remind always notifies.
// Offenders missing from keys are forgotten, which re-arms a reused PID.
func (t *pidIncidents) cycle(now time.Time, keys []pidIncidentKey, fire func(i int, notifyNow bool), remind func(i int)) {
	next := make(map[pidIncidentKey]pidIncidentState, len(keys))
	for i, key := range keys {
		state, fired := t.state[key]
		if key.startTicks != 0 && fired {
			if t.shouldRemind(state, now) {
				remind(i)
				state.lastNotify = now
			}
			next[key] = state
			continue
		}
		notifyNow := !fired || t.shouldRemind(state, now)
		fire(i, notifyNow)
		if notifyNow {
			state.lastNotify = now
		}
		next[key] = state
	}
	t.state = next
}

// processIdentityRule is one declared exact executable identity, optionally
// narrowed by a command-line pattern: process_policy's allow entries and
// unowned_processes' ignore entries are both lists of these.
type processIdentityRule struct {
	filter process.IdentityFilter
	cmd    *regexp.Regexp
}

// newProcessIdentityRules prepares parsed rules into matchers. user narrows
// every rule to one real user; empty leaves the user open.
func newProcessIdentityRules(declared []config.ProcessIdentityRule, user, what string) ([]processIdentityRule, error) {
	out := make([]processIdentityRule, 0, len(declared))
	for _, rule := range declared {
		filter, err := process.NewIdentityFilter(rule.Exe, user, "")
		if err != nil {
			return nil, fmt.Errorf("prepare %s %q: %w", what, rule.Name, err)
		}
		out = append(out, processIdentityRule{filter: filter, cmd: rule.Cmd})
	}
	return out, nil
}

// match reports whether info's exact identity is the rule's (identity) and,
// when it is, whether its command line also satisfies the rule's pattern
// (allowed). An identity match without the pattern is how process_policy tells
// "known binary, unexpected arguments" from an unknown binary.
func (r processIdentityRule) match(info ProcInfo, resolve process.UserResolver) (identity, allowed bool) {
	matched, err := r.filter.Match(info.Identity, resolve, nil)
	if err != nil || matched != process.IdentityMatched {
		return false, false
	}
	return true, r.cmd == nil || r.cmd.MatchString(strings.Join(info.Cmdline, " "))
}

// setProcessWatch is the scaffolding every watch that evaluates the host
// process table as a set shares: identity, delivery, sampling, publishing and
// the per-incarnation incident tracker. process_policy and unowned_processes
// embed it and keep only what differs — how they classify and what they do
// when a finding fires.
type setProcessWatch struct {
	name      string
	checkType string
	summary   string
	check     map[string]any
	notifiers []notify.Notifier
	dryRun    bool
	inPanic   func() bool
	now       func() time.Time
	emit      func(Event)
	sampler   ProcSampler
	publish   func(string, string, checks.Result)
	// severity grades every finding this watch reports.
	severity  severity.Level
	incidents *pidIncidents
}

// newSetProcessWatch wires the shared scaffolding from a resolved watch entry.
func newSetProcessWatch(name, checkType, slot string, entry, checkEntry map[string]any, actions watchActions, deps Deps, level severity.Level) setProcessWatch {
	return setProcessWatch{
		name:      name,
		checkType: checkType,
		summary:   cfgval.String(checkEntry[checks.CheckKeySummary]),
		check:     checkEntry,
		notifiers: resolveNotifiers(actions.effectiveNames, deps.Notifiers),
		dryRun:    config.DryRun(entry),
		inPanic:   deps.Panic.Active,
		now:       deps.Now,
		emit:      deps.Emit,
		sampler:   procSamplerFromDeps(deps),
		publish:   publishWatchSnapshots(deps.WatchSnapshots, deps.watchConfigID),
		severity:  level,
		incidents: newPIDIncidents(name, slot, actions.notifyInterval, level, deps.WatchState, deps.Emit),
	}
}

func (s *setProcessWatch) clock() time.Time { return clockOrNow(s.now)() }

func (s *setProcessWatch) emitEvent(event Event) { emitSafe(s.emit, event) }

// publishResult records this cycle's result for the dashboard, with the
// configured summary applied. A watcher without a registry publishes nothing.
func (s *setProcessWatch) publishResult(result checks.Result) {
	if s.publish == nil {
		return
	}
	result.Check = s.name
	s.publish(s.name, s.checkType, checks.ApplySummary(s.summary, s.check, result))
}

// publishUnavailable records a cycle whose process list could not be read.
func (s *setProcessWatch) publishUnavailable(subject string, data map[string]any) {
	s.publishResult(checks.Result{OK: false, Message: subject + ": sample unavailable", Data: data})
}

// env is the notification environment every finding carries; extra adds the
// watcher's own variables.
func (s *setProcessWatch) env(message string, extra map[string]string) map[string]string {
	env := map[string]string{sermoEnvWatch: s.name, sermoEnvCheckType: s.checkType, sermoEnvMessage: message}
	maps.Copy(env, extra)
	return env
}

// notify delivers one message through the shared dispatcher (dry run and
// panic mode suppress it) and records a live delivery on the open incident,
// which is what decides whether its recovery is announced later.
func (s *setProcessWatch) notify(ctx context.Context, message string, env map[string]string) {
	if len(s.notifiers) == 0 {
		return
	}
	delivered := dispatchWatchFire(ctx, watchFireSpec{
		name:        s.name,
		notifiers:   s.notifiers,
		inPanic:     s.inPanic,
		dryRun:      s.dryRun,
		emit:        s.emitEvent,
		dryRunLabel: watchDryRunMessage(HookSpec{}, s.notifiers),
		panicLabel:  "panic mode: notifications suppressed",
		severity:    s.severity,
	}, message, env)
	if delivered {
		s.incidents.announce()
	}
}

// settle updates the aggregate incident for this cycle's finding count and,
// when the last finding cleared, emits the recovered event and notifies it if
// the incident had been announced live.
func (s *setProcessWatch) settle(ctx context.Context, findings int, message string, extra map[string]string) {
	recovered, announced := s.incidents.settle(findings)
	if !recovered {
		return
	}
	s.emitEvent(Event{Watch: s.name, Kind: eventKindRecovered, Severity: s.severity, Message: message})
	if announced {
		env := s.env(message, extra)
		env[sermoEnvEvent] = eventKindRecovered
		s.notify(ctx, recoveredMessagePrefix+message, env)
	}
}

// incidentKey identifies a sampled process's incarnation.
func (s ProcInfo) incidentKey() pidIncidentKey {
	return pidIncidentKey{pid: s.PID, startTicks: s.StartTicks}
}

// incidentKeys maps this cycle's findings to their incarnation keys.
func incidentKeys[T any](items []T, info func(T) ProcInfo) []pidIncidentKey {
	keys := make([]pidIncidentKey, len(items))
	for i := range items {
		keys[i] = info(items[i]).incidentKey()
	}
	return keys
}

// rejectSetWatchActions is the build-time counterpart of the config validator
// for a set-evaluating watch: the same then vocabulary, so an unchecked Config
// cannot widen a watch's action set.
func rejectSetWatchActions(entry map[string]any, shape config.SetWatchActions) error {
	if _, present := entry[rules.SectionPolicy]; present {
		return errors.New("policy is not valid on an " + shape.Description())
	}
	then, err := thenMap(entry)
	if err != nil || then == nil {
		return err
	}
	for _, key := range slices.Sorted(maps.Keys(then)) {
		if !shape.Accepts(key) {
			return fmt.Errorf("then.%s is not valid on an %s", key, shape.Description())
		}
	}
	return nil
}
