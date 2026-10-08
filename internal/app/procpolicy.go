package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/process"
)

// processPolicyAllow is one exact executable identity permitted for a watched
// real user. Cmd can only narrow that identity; it is never emitted anywhere.
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
	processPolicyStateSlot           = "process-policy"
)

// processPolicyWatcher verifies that every process of one real user belongs to
// an allowlisted executable identity. It is alert-only: it has no hook, signal,
// command runner or other remediation capability.
type processPolicyWatcher struct {
	setProcessWatch
	user    string
	allows  []processIdentityRule
	resolve process.UserResolver
}

// buildProcessPolicyWatch builds an alert-only host watch. Validation enforces
// this in normal configuration loading; the builder repeats it so callers that
// construct an unchecked Config cannot turn the watch into a control path.
func buildProcessPolicyWatch(name string, entry, checkEntry map[string]any, deps Deps, interval time.Duration) (*Watch, string) {
	user := cfgval.String(checkEntry[checks.CheckKeyUser])
	if user == "" {
		return nil, watchSubjectPrefix + name + ": process_policy check requires a user"
	}
	if err := rejectSetWatchActions(entry, config.ProcessPolicyActions); err != nil {
		return nil, watchSubjectPrefix + name + ": " + err.Error()
	}
	allows, err := parseProcessPolicyAllows(user, checkEntry)
	if err != nil {
		return nil, watchSubjectPrefix + name + ": " + err.Error()
	}
	actions, err := resolveWatchActions(entry, deps, watchActionOptions{
		checkType:    checks.CheckTypeProcessPolicy,
		emptyMessage: "then " + config.ProcessPolicyActions.EmptyThenMessage(),
	})
	if err != nil {
		return nil, watchSubjectPrefix + name + ": " + err.Error()
	}
	resolve := process.DefaultUserLookup().ResolveUser
	if deps.UserLookup != nil {
		resolve = deps.UserLookup.ResolveUser
	}
	pw := &processPolicyWatcher{
		setProcessWatch: newSetProcessWatch(name, checks.CheckTypeProcessPolicy, processPolicyStateSlot, entry, checkEntry, actions, deps, watchSeverity(entry, checkEntry)),
		user:            user,
		allows:          allows,
		resolve:         resolve,
	}
	return newStatefulWatch(name, checks.CheckTypeProcessPolicy, entry, deps, interval, pw.runCycle), ""
}

func parseProcessPolicyAllows(user string, check map[string]any) ([]processIdentityRule, error) {
	parsed, issues := config.ParseProcessPolicyAllows(check[checks.CheckKeyAllow])
	if len(issues) > 0 {
		return nil, issues[0]
	}
	return newProcessIdentityRules(parsed, user, "process_policy allow")
}

func (w *processPolicyWatcher) runCycle(ctx context.Context) {
	samples, ok := w.sampler.Sample(ProcMatch{User: w.user})
	if !ok {
		w.publishSnapshot(nil, nil, false)
		return
	}
	slices.SortFunc(samples, func(a, b ProcInfo) int { return cmp.Compare(a.PID, b.PID) })
	violations := make([]processPolicyViolation, 0)
	for i := range samples {
		if ctx.Err() != nil {
			return
		}
		if reason := w.violationReason(samples[i]); reason != "" {
			violations = append(violations, processPolicyViolation{info: samples[i], reason: reason})
		}
	}
	w.publishSnapshot(samples, violations, true)

	if observeOnlyCycle(ctx) {
		return
	}
	w.settle(ctx, len(violations), processPolicySubject(w.user)+": no violations", map[string]string{sermoEnvUser: w.user})
	keys := incidentKeys(violations, func(v processPolicyViolation) ProcInfo { return v.info })
	w.incidents.cycle(w.clock(), keys,
		func(i int, notifyNow bool) { w.fire(ctx, violations[i], notifyNow) },
		func(i int) { w.remind(ctx, violations[i]) })
}

func (w *processPolicyWatcher) violationReason(info ProcInfo) string {
	hasExecutableMatch := false
	for _, allow := range w.allows {
		identity, allowed := allow.match(info, w.resolve)
		if !identity {
			continue
		}
		hasExecutableMatch = true
		if allowed {
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
	if !ok {
		w.publishUnavailable(processPolicySubject(w.user), map[string]any{watchReadingFieldUser: w.user})
		return
	}
	message := fmt.Sprintf("%s: %d active process%s, %d violation%s", processPolicySubject(w.user), len(samples), pluralSuffix(len(samples), "process"), len(violations), pluralSuffix(len(violations), "violation"))
	if len(violations) > 0 {
		message += ": " + processPolicyViolationList(violations)
	}
	w.publishResult(checks.Result{OK: len(violations) == 0, Message: message, Data: processPolicyData(w.user, samples, violations)})
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
	w.emitEvent(Event{Watch: w.name, Kind: eventKindFiring, Severity: w.severity, Message: message})
	if notifyNow {
		w.notify(ctx, message, env)
	}
}

func (w *processPolicyWatcher) remind(ctx context.Context, violation processPolicyViolation) {
	message, env := w.message(violation)
	w.notify(ctx, message, env)
}

func (w *processPolicyWatcher) message(violation processPolicyViolation) (string, map[string]string) {
	message := processPolicySubject(w.user) + ": " + processPolicyViolationText(violation)
	return message, w.env(message, map[string]string{sermoEnvPID: strconv.Itoa(violation.info.PID), sermoEnvUser: w.user})
}
