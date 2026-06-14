package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/notify"
	"sermo/internal/rules"
	"sermo/internal/volume"
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
	Name      string
	CheckType string // e.g. "storage"; for SERMO_CHECK_TYPE (Result.Check is the watch name)
	Check     checks.Check
	Window    rules.Rule // carries only For/Within; used by rules.WindowState.Fires
	Hook      HookSpec
	Runner    HookRunner
	// Notifiers receive a notification when the watch fires (the resolved
	// `then.notify` targets, or the inherited global default).
	Notifiers []notify.Notifier
	// DryRun keeps watch evaluation and firing events active, but reports the
	// configured actions without executing hook, notify or expand side effects.
	DryRun   bool
	Interval time.Duration
	Now      func() time.Time
	Emit     func(Event)
	// IsPaused reports whether this watch is currently paused by an operator.
	// Paused watches skip checks/hooks/notifies/expand until monitored again.
	IsPaused func() bool
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
	// Result's "path" data.
	Expand   *ExpandSpec
	Expander VolumeExpander
	Policy   rules.Policy

	state       rules.WindowState
	policyState rules.RemediationState
}

// RunCycle runs the check, advances the window, and fires the hook on a firing
// cycle. An evaluation/hook error is emitted, never fatal.
func (w *Watch) RunCycle(ctx context.Context) {
	if w.IsPaused != nil && w.IsPaused() {
		return
	}
	if w.Cycle != nil {
		w.Cycle(ctx)
		return
	}
	res := w.Check.Run(ctx)
	fired := res.OK
	if w.FireOnFail {
		fired = !res.OK
	}
	if !w.state.Fires(w.Window, fired) {
		return
	}
	// Always emit a "firing" event when the `for` (or `within`) window is
	// satisfied. This makes the alert visible in the web UI (state=failed,
	// Alerts/Watches counts, failed filter) and in the event log even for
	// bare watches that have no `then` (pure monitor-only / alert-only case).
	w.emit(Event{Watch: w.Name, Kind: "firing", Message: res.Message})
	if w.DryRun {
		w.emit(Event{Watch: w.Name, Kind: "dry-run", Message: w.dryRunMessage()})
		return
	}

	if w.Expand != nil && w.Expander != nil {
		w.runExpand(ctx, res)
	}
	env := hookEnv(w.Name, w.CheckType, res)
	if len(w.Hook.Command) > 0 {
		runner := defaultHookRunner(w.Runner)
		if err := w.Hook.Run(ctx, runner, env); err != nil {
			w.emit(Event{Watch: w.Name, Kind: "hook-failed", Message: err.Error()})
		} else {
			w.emit(Event{Watch: w.Name, Kind: "hook", Message: res.Message})
		}
	}
	dispatchNotify(ctx, w.Notifiers, watchMessage(w.Name, res.Message, env), w.Name, w.emit)
}

// runExpand performs the native storage-expansion action on a firing cycle, gated
// by Policy. The action is attempted at most once per cooldown window even while
// the volume stays low; an attempt (success or failure) records the time so a
// failing expansion is not retried every cycle.
func (w *Watch) runExpand(ctx context.Context, res checks.Result) {
	now := time.Now
	if w.Now != nil {
		now = w.Now
	}
	at := now()
	if allowed, reason := w.Policy.Allow(&w.policyState, at); !allowed {
		w.emit(Event{Watch: w.Name, Kind: "expand-skipped", Message: reason})
		return
	}
	path := cfgval.String(res.Data["path"])
	r, err := w.Expander.ExpandPath(ctx, path, w.Expand.By)
	w.policyState.Record(at, w.Policy)
	if err != nil {
		w.emit(Event{Watch: w.Name, Kind: "expand-failed", Message: err.Error()})
		return
	}
	w.emit(Event{Watch: w.Name, Kind: "expand", Message: expandSuccessMessage(path, r)})
}

func expandSuccessMessage(path string, r volume.Result) string {
	return fmt.Sprintf("%s: grew %s/%s by %d bytes", path, r.VG, r.LV, r.GrewBytes)
}

func watchDryRunMessage(hook HookSpec, notifiers []notify.Notifier, expand *ExpandSpec) string {
	actions := make([]string, 0, 3)
	if expand != nil {
		actions = append(actions, "expand")
	}
	if len(hook.Command) > 0 {
		actions = append(actions, "hook")
	}
	if len(notifiers) > 0 {
		actions = append(actions, "notify")
	}
	if len(actions) == 0 {
		return "dry-run: no configured watch actions"
	}
	return "dry-run: would run " + strings.Join(actions, ", ")
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
	if w.Emit != nil {
		w.Emit(e)
	}
}

// dispatchNotify delivers msg to each notifier, emitting one event per result. A
// failed delivery is reported but never aborts the cycle (other targets and the
// hook still run) — notifications are best-effort.
func dispatchNotify(ctx context.Context, notifiers []notify.Notifier, msg notify.Message, watch string, emit func(Event)) {
	for _, n := range notifiers {
		if err := n.Send(ctx, msg); err != nil {
			emit(Event{Watch: watch, Kind: "notify-failed", Message: n.Name() + ": " + err.Error()})
		} else {
			emit(Event{Watch: watch, Kind: "notify", Message: "notified " + n.Name()})
		}
	}
}

// watchMessage builds a notification from a fired watch's message and hook env.
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
		body.WriteString(k + "=" + env[k] + "\n")
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
		"SERMO_WATCH":      name,
		"SERMO_CHECK_TYPE": checkType,
		"SERMO_MESSAGE":    checks.TrimOutput(res.Message),
	}
	for k, v := range res.Data {
		env["SERMO_"+envKey(k)] = checks.TrimOutput(cfgval.String(v))
	}
	return env
}

// envKey uppercases a Data key and replaces any non-alphanumeric rune with "_".
func envKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
