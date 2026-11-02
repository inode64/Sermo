package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"sermo/internal/web"
)

const (
	// commandWatchKill is `sermoctl watch kill WATCH PID`.
	commandWatchKill = "kill"
	// watchKillArgCount is `kill WATCH PID`.
	watchKillArgCount = 3
)

// runWatchKill signals one process an unowned_processes watch lists, the
// dashboard's row kill from the command line. Like the button it only names a
// displayed incarnation: the PID and the start ticks of the daemon's listing go
// to the daemon, which re-reads the process and decides through the same kill
// gate. sermoctl never signals anything itself.
func (a App) runWatchKill(ctx context.Context, opts options) int {
	if len(opts.args) != watchKillArgCount {
		return a.commandUsageError(commandWatch, "watch kill requires WATCH PID")
	}
	name := opts.args[1]
	pid, err := strconv.Atoi(opts.args[2])
	if err != nil || pid <= 0 {
		return a.commandUsageError(commandWatch, fmt.Sprintf("invalid pid %q", opts.args[2]))
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	watch, ok := a.fetchDaemonWatchDetail(ctx, cfg, name)
	if !ok {
		return a.fail(opts, fmt.Sprintf("watch kill: the daemon lists no watch %q", name))
	}
	row, found := findWatchProcess(watch, pid)
	if !found {
		return a.fail(opts, fmt.Sprintf("pid %d is not listed by %s; refresh with sermoctl watch refresh %s", pid, name, name))
	}
	if !row.CanKill {
		return a.fail(opts, fmt.Sprintf("pid %d of %s cannot be killed: %s", pid, name, row.KillReason))
	}
	query := url.Values{}
	query.Set(web.APIQueryStartTicks, strconv.FormatUint(row.StartTicks, 10))
	if opts.escalate {
		query.Set(web.APIQueryEscalate, strconv.FormatBool(true))
	}
	killCtx, cancel := context.WithTimeout(ctx, daemonActionTimeout(opts, manualOperationTimeout(cfg, opts)))
	defer cancel()
	result, status, err := a.postDaemonAction(killCtx, cfg, commandWatch+" "+commandWatchKill, web.WatchProcessKillPath(name, pid)+"?"+query.Encode())
	if err != nil {
		return a.fail(opts, "watch kill: "+err.Error())
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyWatch: name, cliJSONKeyPID: pid, cliJSONKeyOK: result.OK, cliJSONKeyMessage: result.Message})
	} else {
		label := cliTextOK
		if !result.OK {
			label = cliTextFail
		}
		fmt.Fprintf(a.Stdout, "%s kill pid %d on %s: %s\n", label, pid, name, result.Message)
	}
	switch {
	case result.OK:
		return exitSuccess
	case status == http.StatusConflict:
		// The daemon refused: the process changed, is no longer listed or its
		// identity no longer verifies. Same exit as other blocked operations.
		return exitBlocked
	default:
		return exitRuntimeError
	}
}

func findWatchProcess(watch web.Watch, pid int) (web.WatchProcess, bool) {
	for _, row := range watch.Processes {
		if row.PID == pid {
			return row, true
		}
	}
	return web.WatchProcess{}, false
}
