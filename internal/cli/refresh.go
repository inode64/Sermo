package cli

import (
	"context"
	"fmt"

	"sermo/internal/config"
	"sermo/internal/web"
)

// runDaemonRefresh asks the running daemon to run the watches behind one
// listing now, besides their own schedule, and prints its outcome. The daemon
// waits for those cycles within engine.operation_timeout; the command waits
// for that answer.
func (a App) runDaemonRefresh(ctx context.Context, opts options, label, path string) int {
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	refreshCtx, cancel := context.WithTimeout(ctx, daemonActionTimeout(opts, manualOperationTimeout(cfg, opts)))
	defer cancel()
	result, err := a.RefreshDaemon(refreshCtx, cfg, path)
	if err != nil {
		return a.fail(opts, label+": "+err.Error())
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyOK: result.OK, cliJSONKeyMessage: result.Message})
	} else {
		status := cliTextOK
		if !result.OK {
			status = cliTextFail
		}
		fmt.Fprintf(a.Stdout, "%s %s: %s\n", status, label, result.Message)
	}
	if result.OK {
		return exitSuccess
	}
	return exitNotActive
}

// refreshDaemon posts one refresh. A 409 is the daemon's answer, not a failed
// request: the refresh ran but did not complete (a cycle still running at the
// deadline, a watch with no running cycle), or it refused a name.
func (a App) refreshDaemon(ctx context.Context, cfg *config.Config, path string) (web.ActionResult, error) {
	result, _, err := a.postDaemonAction(ctx, cfg, commandArgRefresh, path)
	return result, err
}
