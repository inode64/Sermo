package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"sermo/internal/state"
)

// runPanic enables, disables or reports the daemon-wide panic mode. While panic
// mode is on the daemon keeps monitoring (so status stays visible) but suspends
// hooks, alerts and automatic remediation. The flag lives in the persistent
// store under paths.state, so it survives daemon restarts and the daemon picks
// up the change within a second — no running web UI is required.
func (a App) runPanic(opts options) int {
	sub := ""
	if len(opts.args) > 0 {
		sub = strings.ToLower(opts.args[0])
	}
	if len(opts.args) > 1 {
		return a.commandUsageError("panic", "panic takes at most one argument: on, off or status")
	}

	var set, on bool
	switch sub {
	case "on", "enable":
		set, on = true, true
	case "off", "disable":
		set, on = true, false
	case "", "status":
		set = false
	default:
		return a.commandUsageError("panic", fmt.Sprintf("unknown panic argument %q (use on, off or status)", sub))
	}

	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}

	store, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		return a.fail(opts, fmt.Sprintf("panic failed: %v", err))
	}
	defer store.Close()

	if set {
		cmd := "panic off"
		if on {
			cmd = "panic on"
		}
		if err := store.SetPanic(on, state.SourceCLI); err != nil {
			a.recordAccess(cfg, cmd, "", "error", err.Error())
			return a.fail(opts, fmt.Sprintf("panic failed: %v", err))
		}
		a.recordAccess(cfg, cmd, "", "ok", "")
	}

	rec, found, err := store.Panic()
	if err != nil {
		return a.fail(opts, fmt.Sprintf("panic failed: %v", err))
	}
	a.reportPanic(opts, rec, found)
	return exitSuccess
}

func (a App) reportPanic(opts options, rec state.GlobalRecord, found bool) {
	enabled := found && rec.On
	if opts.json {
		payload := map[string]any{"panic": enabled}
		if found {
			if rec.Source != "" {
				payload["panic_source"] = rec.Source
			}
			if !rec.UpdatedAt.IsZero() {
				payload["panic_changed_at"] = rec.UpdatedAt.UTC().Format(time.RFC3339)
			}
		}
		writeJSON(a.Stdout, payload)
		return
	}
	changedAt := ""
	if found && !rec.UpdatedAt.IsZero() {
		changedAt = rec.UpdatedAt.UTC().Format(time.RFC3339)
	}
	state := "off"
	if enabled {
		state = "on (hooks, alerts and automatic remediation suspended)"
	}
	suffix := ""
	if found {
		suffix = metaSuffix(rec.Source, changedAt)
	}
	fmt.Fprintf(a.Stdout, "panic mode: %s%s\n", state, suffix)
}
