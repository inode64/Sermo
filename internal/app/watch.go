package app

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/checks"
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
	Interval  time.Duration
	Now       func() time.Time
	Emit      func(Event)

	state rules.WindowState
}

// RunCycle runs the check, advances the window, and fires the hook on a firing
// cycle. An evaluation/hook error is emitted, never fatal.
func (w *Watch) RunCycle(ctx context.Context) {
	res := w.Check.Run(ctx)
	if !w.state.Fires(w.Window, res.OK) {
		return
	}
	runner := w.Runner
	if runner == nil {
		runner = OSHookRunner{}
	}
	env := hookEnv(w.Name, w.CheckType, res)
	if err := w.Hook.Run(ctx, runner, env); err != nil {
		w.emit(Event{Watch: w.Name, Kind: "hook-failed", Message: err.Error()})
		return
	}
	w.emit(Event{Watch: w.Name, Kind: "hook", Message: res.Message})
}

func (w *Watch) emit(e Event) {
	if w.Emit != nil {
		w.Emit(e)
	}
}

// hookEnv builds the SERMO_* environment for a hook from the check result.
// checkType is the configured check type (e.g. "disk"); res.Check is the watch
// name (base.name), so it must not be used for SERMO_CHECK_TYPE.
func hookEnv(name, checkType string, res checks.Result) map[string]string {
	env := map[string]string{
		"SERMO_WATCH":      name,
		"SERMO_CHECK_TYPE": checkType,
		"SERMO_MESSAGE":    res.Message,
	}
	if p, ok := res.Data["path"].(string); ok {
		env["SERMO_PATH"] = p
	}
	if v, ok := res.Data["used_pct"]; ok {
		env["SERMO_VALUE"] = fmt.Sprintf("%v", v)
	}
	return env
}
