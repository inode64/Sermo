package app

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"sermo/internal/checks"
	"sermo/internal/notify"
	"sermo/internal/rules"
)

// Watch monitors one host resource: each cycle it runs its check, advances its
// window, and fires its hook when the condition (check.OK) holds for the window.
// It is independent of services and does not use the operation engine.
type Watch struct {
	Name      string
	CheckType string // e.g. "disk"; for SERMO_CHECK_TYPE (Result.Check is the watch name)
	Check     checks.Check
	Window    rules.Rule // carries only For/Within; used by rules.WindowState.Fires
	Hook      HookSpec
	Runner    HookRunner
	// Notifiers receive a notification when the watch fires (the resolved
	// `then.notify` targets). A watch must have a hook and/or at least one notifier.
	Notifiers []notify.Notifier
	Interval  time.Duration
	Now       func() time.Time
	Emit      func(Event)
	// Cycle, when set, replaces the default single-check/single-hook behavior.
	// Stateful multi-target watches (e.g. the file watch) use it to fire one hook
	// per detected change within a cycle, which the one-Result model cannot express.
	Cycle func(ctx context.Context)
	// FireOnFail inverts the trigger: the hook fires when the check is NOT OK,
	// instead of when it is. Health checks (tcp/http/…) are healthy at OK==true, so
	// as a watch they alert on failure; condition checks (disk/load/…) alert at
	// OK==true (threshold crossed) and leave this false.
	FireOnFail bool

	state rules.WindowState
}

// RunCycle runs the check, advances the window, and fires the hook on a firing
// cycle. An evaluation/hook error is emitted, never fatal.
func (w *Watch) RunCycle(ctx context.Context) {
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
		"SERMO_MESSAGE":    res.Message,
	}
	for k, v := range res.Data {
		env["SERMO_"+envKey(k)] = stringifyValue(v)
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

// stringifyValue renders a Data value; whole floats print without a trailing .0.
func stringifyValue(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}
