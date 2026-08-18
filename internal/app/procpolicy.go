package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/process"
	"sermo/internal/rules"
)

// processPolicyAllow is one exact executable identity permitted for a watched
// real user. Cmd can only narrow that identity; it is never emitted anywhere.
type processPolicyAllow struct {
	filter process.IdentityFilter
	cmd    *regexp.Regexp
}

// processPolicyKey identifies one sampled process. Non-zero start ticks keep a
// recycled PID from inheriting the previous process's alert edge state.
type processPolicyKey struct {
	pid        int
	startTicks uint64
}

// processPolicyState retains notification cadence for a current violation. A
// zero StartTicks value cannot establish a PID incarnation, so it paces delivery
// without suppressing a fresh firing event from a potentially reused PID.
type processPolicyState struct {
	lastNotify time.Time
}

// processPolicyViolation is deliberately presentation-safe: it names a PID and
// resolved executable state, never the process command line or its arguments.
type processPolicyViolation struct {
	info   ProcInfo
	reason string
}

const (
	processPolicyMessagePrefix       = "process policy user "
	processPolicyReasonReplacedExe   = "replaced executable is unresolved"
	processPolicyReasonUnresolvedExe = "executable is unresolved"
	processPolicyReasonCommand       = "command does not match the execution policy"
	processPolicyReasonExecutable    = "executable is not allowlisted"
)

// processPolicyWatcher verifies that every process of one real user belongs to
// an allowlisted executable identity. It is alert-only: it has no hook, signal,
// command runner or other remediation capability.
type processPolicyWatcher struct {
	name           string
	user           string
	allows         []processPolicyAllow
	summary        string
	check          map[string]any
	notifiers      []notify.Notifier
	notifyInterval time.Duration
	dryRun         bool
	inPanic        func() bool
	now            func() time.Time
	emit           func(Event)
	sampler        ProcSampler
	resolve        process.UserResolver
	publish        func(string, string, checks.Result)

	state map[processPolicyKey]processPolicyState
}

// buildProcessPolicyWatch builds an alert-only host watch. Validation enforces
// this in normal configuration loading; the builder repeats it so callers that
// construct an unchecked Config cannot turn the watch into a control path.
func buildProcessPolicyWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	user := cfgval.String(checkEntry[checks.CheckKeyUser])
	if user == "" {
		return nil, watchSubjectPrefix + name + ": process_policy check requires a user"
	}
	if err := rejectProcessPolicyActions(entry); err != nil {
		return nil, watchSubjectPrefix + name + ": " + err.Error()
	}
	allows, err := parseProcessPolicyAllows(user, checkEntry)
	if err != nil {
		return nil, watchSubjectPrefix + name + ": " + err.Error()
	}
	actions, err := resolveWatchActions(entry, deps, watchActionOptions{
		checkType:    checks.CheckTypeProcessPolicy,
		emptyMessage: "then requires notify or omit then for dashboard/event-log alerts",
	})
	if err != nil {
		return nil, watchSubjectPrefix + name + ": " + err.Error()
	}
	resolve := process.DefaultUserLookup().ResolveUser
	if deps.UserLookup != nil {
		resolve = deps.UserLookup.ResolveUser
	}
	pw := &processPolicyWatcher{
		name:           name,
		user:           user,
		allows:         allows,
		summary:        cfgval.String(checkEntry[checks.CheckKeySummary]),
		check:          checkEntry,
		notifiers:      resolveNotifiers(actions.effectiveNames, deps.Notifiers),
		notifyInterval: actions.notifyInterval,
		dryRun:         config.DryRun(entry),
		inPanic:        deps.Panic.Active,
		now:            deps.Now,
		emit:           deps.Emit,
		sampler:        procSamplerFromDeps(deps),
		resolve:        resolve,
		publish:        publishWatchSnapshots(deps.WatchSnapshots),
	}
	return newStatefulWatch(name, checks.CheckTypeProcessPolicy, entry, deps, interval, pw.runCycle), ""
}

// rejectProcessPolicyActions is the build-time counterpart to the config
// validator. Notifications are the only optional delivery mechanism; a missing
// then block keeps the dashboard/event-log-only mode.
func rejectProcessPolicyActions(entry map[string]any) error {
	if _, present := entry[rules.SectionPolicy]; present {
		return errors.New("policy is not valid on an alert-only process_policy watch")
	}
	then, err := thenMap(entry)
	if err != nil || then == nil {
		return err
	}
	for _, key := range slices.Sorted(maps.Keys(then)) {
		if !config.IsAlertOnlyWatchThenKey(key) {
			return fmt.Errorf("then.%s is not valid on an alert-only process_policy watch", key)
		}
	}
	return nil
}

func parseProcessPolicyAllows(user string, check map[string]any) ([]processPolicyAllow, error) {
	rawAllows, ok := check[checks.CheckKeyAllow].(map[string]any)
	if !ok || len(rawAllows) == 0 {
		return nil, fmt.Errorf("process_policy check requires a non-empty %s mapping", checks.CheckKeyAllow)
	}
	allows := make([]processPolicyAllow, 0, len(rawAllows))
	for _, name := range slices.Sorted(maps.Keys(rawAllows)) {
		raw, ok := rawAllows[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("process_policy allow %q must be a mapping", name)
		}
		exe := cfgval.String(raw[checks.CheckKeyExe])
		filter, err := process.NewIdentityFilter(exe, user, "")
		if err != nil || exe == "" || !filepath.IsAbs(exe) || filepath.Clean(exe) != exe {
			return nil, fmt.Errorf("process_policy allow %q requires a clean absolute executable path", name)
		}
		allow := processPolicyAllow{filter: filter}
		if rawCmd, present := raw[process.SelectorKeyCmd]; present {
			cmd, ok := rawCmd.(string)
			if !ok || cmd == "" || !strings.HasPrefix(cmd, "^") || !strings.HasSuffix(cmd, "$") {
				return nil, fmt.Errorf("process_policy allow %q cmd must be a non-empty expression anchored with ^ and $", name)
			}
			allow.cmd, err = regexp.Compile(cmd)
			if err != nil {
				return nil, fmt.Errorf("process_policy allow %q cmd is invalid: %w", name, err)
			}
		}
		allows = append(allows, allow)
	}
	return allows, nil
}

func (w *processPolicyWatcher) runCycle(ctx context.Context) {
	sampler := w.sampler
	if sampler == nil {
		sampler = osProcSampler{}
	}
	samples, ok := sampler.Sample(ProcMatch{User: w.user})
	if !ok {
		w.publishSnapshot(nil, nil, false)
		return
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].PID < samples[j].PID })
	violations := make([]processPolicyViolation, 0)
	for _, sample := range samples {
		if ctx.Err() != nil {
			return
		}
		if reason := w.violationReason(sample); reason != "" {
			violations = append(violations, processPolicyViolation{info: sample, reason: reason})
		}
	}
	w.publishSnapshot(samples, violations, true)

	if observeOnlyCycle(ctx) {
		return
	}
	now := w.clock()
	next := make(map[processPolicyKey]processPolicyState, len(violations))
	for _, violation := range violations {
		key := processPolicyKey{pid: violation.info.PID, startTicks: violation.info.StartTicks}
		state, fired := w.state[key]
		if key.startTicks != 0 && fired {
			if w.shouldRemind(state, now) {
				w.notify(ctx, violation)
				state.lastNotify = now
			}
			next[key] = state
			continue
		}
		notifyNow := !fired || w.shouldRemind(state, now)
		w.fire(ctx, violation, notifyNow)
		if notifyNow {
			state.lastNotify = now
		}
		next[key] = state
	}
	w.state = next
}

func (w *processPolicyWatcher) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

func (w *processPolicyWatcher) shouldRemind(state processPolicyState, now time.Time) bool {
	return w.notifyInterval > 0 && now.Sub(state.lastNotify) >= w.notifyInterval
}

func (w *processPolicyWatcher) violationReason(info ProcInfo) string {
	id := process.Identity{
		PID: info.PID, UID: info.UID, Exe: info.Exe, ExeOK: info.ExeOK,
		ExePrev: info.ExePrev, Cmdline: info.Cmdline,
	}
	hasExecutableMatch := false
	for _, allow := range w.allows {
		matched, err := allow.filter.Match(id, w.resolve, nil)
		if err != nil || matched != process.IdentityMatched {
			continue
		}
		hasExecutableMatch = true
		if allow.cmd == nil || allow.cmd.MatchString(strings.Join(info.Cmdline, " ")) {
			return ""
		}
	}
	if !info.ExeOK {
		if info.ExePrev != "" {
			return processPolicyReasonReplacedExe
		}
		return processPolicyReasonUnresolvedExe
	}
	if hasExecutableMatch {
		return processPolicyReasonCommand
	}
	return processPolicyReasonExecutable
}

func (w *processPolicyWatcher) publishSnapshot(samples []ProcInfo, violations []processPolicyViolation, ok bool) {
	if w.publish == nil {
		return
	}
	if !ok {
		w.publish(w.name, checks.CheckTypeProcessPolicy, checks.Result{
			Check:   w.name,
			OK:      false,
			Message: processPolicySubject(w.user) + ": sample unavailable",
			Data:    map[string]any{watchReadingFieldUser: w.user},
		})
		return
	}
	data := processPolicyData(w.user, samples, violations)
	message := fmt.Sprintf("%s: %d active process%s, %d violation%s", processPolicySubject(w.user), len(samples), pluralSuffix(len(samples), "process"), len(violations), pluralSuffix(len(violations), "violation"))
	if len(violations) > 0 {
		message += ": " + processPolicyViolationList(violations)
	}
	result := checks.Result{
		Check:   w.name,
		OK:      len(violations) == 0,
		Message: message,
		Data:    data,
	}
	w.publish(w.name, checks.CheckTypeProcessPolicy, checks.ApplySummary(w.summary, w.check, result))
}

func processPolicyData(user string, samples []ProcInfo, violations []processPolicyViolation) map[string]any {
	return map[string]any{
		watchReadingFieldUser:        user,
		watchReadingFieldMatches:     len(samples),
		checks.DataKeyViolationCount: len(violations),
		checks.DataKeyViolations:     processPolicyViolationList(violations),
		checks.DataKeyPIDs:           processPolicyViolationPIDs(violations),
	}
}

func processPolicyViolationList(violations []processPolicyViolation) string {
	return limitedDisplayList(violations, processPolicyViolationText)
}

func processPolicyViolationPIDs(violations []processPolicyViolation) string {
	return limitedDisplayList(violations, func(violation processPolicyViolation) string {
		return strconv.Itoa(violation.info.PID)
	})
}

func processPolicyViolationText(violation processPolicyViolation) string {
	message := fmt.Sprintf("pid %d: %s", violation.info.PID, violation.reason)
	if violation.info.ExeOK {
		message += " (" + violation.info.Exe + ")"
	}
	return message
}

func processPolicySubject(user string) string {
	return processPolicyMessagePrefix + user
}

func (w *processPolicyWatcher) fire(ctx context.Context, violation processPolicyViolation, notifyNow bool) {
	message, env := w.message(violation)
	w.emitEvent(Event{Watch: w.name, Kind: eventKindFiring, Message: message})
	if notifyNow {
		w.notifyMessage(ctx, message, env)
	}
}

func (w *processPolicyWatcher) notify(ctx context.Context, violation processPolicyViolation) {
	message, env := w.message(violation)
	w.notifyMessage(ctx, message, env)
}

func (w *processPolicyWatcher) message(violation processPolicyViolation) (string, map[string]string) {
	message := processPolicySubject(w.user) + ": " + processPolicyViolationText(violation)
	env := map[string]string{
		sermoEnvPID:       strconv.Itoa(violation.info.PID),
		sermoEnvUser:      w.user,
		sermoEnvWatch:     w.name,
		sermoEnvCheckType: checks.CheckTypeProcessPolicy,
		sermoEnvMessage:   message,
	}
	return message, env
}

func (w *processPolicyWatcher) notifyMessage(ctx context.Context, message string, env map[string]string) {
	dispatchWatchFire(ctx, watchFireSpec{
		name:        w.name,
		notifiers:   w.notifiers,
		inPanic:     w.inPanic,
		dryRun:      w.dryRun,
		emit:        w.emitEvent,
		dryRunLabel: watchDryRunMessage(HookSpec{}, w.notifiers),
		panicLabel:  "panic mode: notifications suppressed",
	}, message, env)
}

func (w *processPolicyWatcher) emitEvent(event Event) { emitSafe(w.emit, event) }
