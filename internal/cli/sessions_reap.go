package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"sermo/internal/config"
	"sermo/internal/web"
)

const sessionsReapArgCount = 3 // reap SERVICE PID

func (a App) runSessionsReap(ctx context.Context, opts options) int {
	if len(opts.args) != sessionsReapArgCount || opts.connection {
		return a.commandUsageError(commandSessions, "sessions reap requires SERVICE PID")
	}
	pid, err := strconv.Atoi(opts.args[2])
	if err != nil || pid <= 1 {
		return a.commandUsageError(commandSessions, "invalid stray process pid")
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	service, ok := cfg.CanonicalServiceName(opts.args[1])
	if !ok {
		return a.fail(opts, "unknown service "+opts.args[1])
	}
	inventory, err := a.fetchDaemonSessions(ctx, cfg)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	for _, row := range inventory.Strays {
		if row.Service != service || row.PID != pid {
			continue
		}
		if !row.CanClose {
			return a.fail(opts, "selected reap blocked: "+row.CloseReason)
		}
		return a.reapDaemonProcess(ctx, cfg, opts, row)
	}
	return a.fail(opts, "process is not listed as a stray; refresh with sermoctl sessions "+service)
}

func (a App) reapDaemonProcess(ctx context.Context, cfg *config.Config, opts options, row web.StraySession) int {
	query := url.Values{}
	query.Set(web.APIQueryStartTicks, strconv.FormatUint(row.StartTicks, 10))
	query.Set(web.APIQueryIdentity, row.Identity)
	path := web.APIPathServices + "/" + url.PathEscape(row.Service) + "/strays/" + strconv.Itoa(row.PID) + "/reap?" + query.Encode()
	resp, err := a.daemonWebDo(ctx, cfg, http.MethodPost, commandReap, true, func(base string) string { return base + path })
	if err != nil {
		return a.fail(opts, err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	var result web.ActionResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return a.fail(opts, "decode selected reap response: "+err.Error())
	}
	if opts.json {
		writeJSON(a.Stdout, result)
	} else {
		fmt.Fprintf(a.Stdout, "reap %s pid %d: %s\n", row.Service, row.PID, result.Message)
	}
	if resp.StatusCode == http.StatusOK && result.OK {
		return exitSuccess
	}
	return exitBlocked
}

func (a App) writeStraySessions(rows []web.StraySession) {
	tw := newTabWriter(a.Stdout)
	fmt.Fprintln(tw, "SERVICE\tUSER\tPID\tPPID\tEXECUTABLE\tCLOSE")
	for _, row := range rows {
		exe := row.Exe
		if row.ExePrevious != "" {
			exe = row.ExePrevious + " (deleted)"
		}
		reason := row.CloseReason
		if row.CanClose {
			reason = "authorized"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\n", row.Service, row.User, row.PID, row.PPID, exe, reason)
	}
	_ = tw.Flush()
}
