package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/rules"
	"sermo/internal/units"
	"sermo/internal/web"
)

const (
	commandSessionsList = "list"
	commandSessionsKill = "kill"
)

// sessionsKillArgCount is `kill SERVICE WATCH ID`.
const sessionsKillArgCount = 4

const (
	sessionsQueryColumnWidth   = 60
	sessionsTruncationEllipsis = "…"
	sessionsEmptyCell          = "-"
	sessionsLongMarker         = "LONG"
	// sessionsStoppingMarker flags a statement the server is already stopping.
	sessionsStoppingMarker = "STOPPING"
)

// runSessions lists the daemon's interactive sessions and running database
// statements, or kills one listed statement. The CLI never opens a database
// connection: listing reads the daemon's inventory and a kill goes through the
// daemon, which re-verifies the statement through the service's operation engine.
func (a App) runSessions(ctx context.Context, opts options) int {
	args := opts.args
	if len(args) > 0 && args[0] == commandSessionsKill {
		return a.runSessionsKill(ctx, opts)
	}
	if opts.connection {
		return a.commandUsageError(commandSessions, "--connection is only supported by sessions kill")
	}
	if len(args) > 0 && args[0] == commandSessionsList {
		args = args[1:]
	}
	if len(args) > 1 {
		return a.commandUsageError(commandSessions, "sessions list accepts at most one service name")
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	service := ""
	if len(args) == 1 {
		canonical, ok := cfg.CanonicalServiceName(args[0])
		if !ok {
			return a.fail(opts, fmt.Sprintf("unknown service %q", args[0]))
		}
		service = canonical
	}
	inventory, err := a.fetchDaemonSessions(ctx, cfg)
	if err != nil {
		return a.fail(opts, "sessions: "+err.Error())
	}
	inventory = filterSessionInventory(inventory, service)
	if opts.json {
		writeJSON(a.Stdout, inventory)
		return exitSuccess
	}
	a.writeSessionInventory(inventory)
	return exitSuccess
}

func (a App) runSessionsKill(ctx context.Context, opts options) int {
	if len(opts.args) != sessionsKillArgCount {
		return a.commandUsageError(commandSessions, "sessions kill requires SERVICE WATCH ID")
	}
	watch := opts.args[2]
	id, err := strconv.ParseInt(opts.args[3], 10, 64)
	if err != nil || id <= 0 {
		return a.commandUsageError(commandSessions, fmt.Sprintf("invalid statement id %q", opts.args[3]))
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	service, ok := cfg.CanonicalServiceName(opts.args[1])
	if !ok {
		return a.fail(opts, fmt.Sprintf("unknown service %q", opts.args[1]))
	}
	inventory, err := a.fetchDaemonSessions(ctx, cfg)
	if err != nil {
		return a.fail(opts, "sessions kill: "+err.Error())
	}
	row, found := findDBQuerySession(inventory, service, watch, id)
	if !found {
		return a.fail(opts, fmt.Sprintf("statement %d not listed by %s:%s; refresh with sermoctl sessions %s", id, service, watch, service))
	}
	if !row.CanKill || row.Identity == "" {
		return a.fail(opts, fmt.Sprintf("statement %d of %s:%s cannot be killed (no exact identity)", id, service, watch))
	}
	mode := checks.DBQueryKillModeQuery
	if opts.connection {
		mode = checks.DBQueryKillModeConnection
	}
	result, status, err := a.killDaemonDBQuery(ctx, cfg, row, mode)
	if err != nil {
		return a.fail(opts, "sessions kill: "+err.Error())
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]any{
			cliJSONKeyService: service, cliJSONKeyWatch: watch, web.APIQueryID: id, web.APIQueryMode: mode,
			cliJSONKeyOK: result.OK, cliJSONKeyMessage: result.Message,
		})
	} else {
		label := cliTextOK
		if !result.OK {
			label = cliTextFail
		}
		fmt.Fprintf(a.Stdout, "%s kill %s %d on %s:%s: %s\n", label, mode, id, service, watch, result.Message)
	}
	switch {
	case result.OK:
		return exitSuccess
	case status == http.StatusConflict:
		// The daemon refused: a guard, panic mode, a lock, or the statement is
		// gone. Same exit as other operations blocked by the safety engine.
		return exitBlocked
	default:
		return exitRuntimeError
	}
}

// fetchDaemonSessions reads the daemon's session inventory. Unlike the
// best-effort enrichment helpers, a missing daemon is an error here: the
// inventory is the command's whole output.
func (a App) fetchDaemonSessions(ctx context.Context, cfg *config.Config) (web.SessionInventory, error) {
	body, status, err := a.daemonAPIGetWithConfig(ctx, cfg, web.APIPathSessions)
	if err != nil {
		return web.SessionInventory{}, err
	}
	if status != http.StatusOK {
		return web.SessionInventory{}, fmt.Errorf("daemon answered %d %s%s", status, strings.TrimSpace(string(body)), daemonWebStatusHint(status))
	}
	var inventory web.SessionInventory
	if err := json.Unmarshal(body, &inventory); err != nil {
		return web.SessionInventory{}, fmt.Errorf("decode sessions response: %w", err)
	}
	return inventory, nil
}

// killDaemonDBQuery posts the identity the inventory displayed. The daemon
// answers 200 for a kill and 409 for a refusal, both with an ActionResult.
func (a App) killDaemonDBQuery(ctx context.Context, cfg *config.Config, row web.DBQuerySession, mode string) (web.ActionResult, int, error) {
	query := url.Values{}
	query.Set(web.APIQueryID, strconv.FormatInt(row.ID, 10))
	query.Set(web.APIQueryIdentity, row.Identity)
	query.Set(web.APIQueryMode, mode)
	path := web.DBQueryKillPath(row.Service, row.Watch) + "?" + query.Encode()
	resp, err := a.daemonWebDo(ctx, cfg, http.MethodPost, string(rules.ActionKillQuery), true, func(base string) string {
		return base + path
	})
	if err != nil {
		return web.ActionResult{}, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	var result web.ActionResult
	decodeErr := json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusConflict {
		if decodeErr != nil {
			return web.ActionResult{}, resp.StatusCode, fmt.Errorf("decode kill response: %w", decodeErr)
		}
		return result, resp.StatusCode, nil
	}
	message := strings.TrimSpace(result.Message)
	if message == "" {
		message = resp.Status
	}
	return web.ActionResult{}, resp.StatusCode, fmt.Errorf("kill failed (%d): %s%s", resp.StatusCode, message, daemonWebStatusHint(resp.StatusCode))
}

func findDBQuerySession(inventory web.SessionInventory, service, watch string, id int64) (web.DBQuerySession, bool) {
	for _, row := range inventory.Database {
		if row.Service == service && row.Watch == watch && row.ID == id {
			return row, true
		}
	}
	return web.DBQuerySession{}, false
}

// filterSessionInventory keeps the rows and sources of one service; an empty
// service keeps everything. Slices stay non-nil so --json prints [] not null.
func filterSessionInventory(inventory web.SessionInventory, service string) web.SessionInventory {
	out := web.SessionInventory{
		Sources:  []web.SessionSource{},
		SSH:      []web.SSHSession{},
		Terminal: []web.TerminalSession{},
		Database: []web.DBQuerySession{},
	}
	keep := func(s string) bool { return service == "" || s == service }
	for _, source := range inventory.Sources {
		if keep(source.Service) {
			out.Sources = append(out.Sources, source)
		}
	}
	for _, row := range inventory.SSH {
		if keep(row.Service) {
			out.SSH = append(out.SSH, row)
		}
	}
	for _, row := range inventory.Terminal {
		if keep(row.Service) {
			out.Terminal = append(out.Terminal, row)
		}
	}
	for _, row := range inventory.Database {
		if keep(row.Service) {
			out.Database = append(out.Database, row)
		}
	}
	return out
}

func (a App) writeSessionInventory(inventory web.SessionInventory) {
	printed := false
	section := func(title string) {
		if printed {
			fmt.Fprintln(a.Stdout)
		}
		printed = true
		fmt.Fprintln(a.Stdout, title)
	}
	if len(inventory.SSH) > 0 {
		section("SSH sessions:")
		tw := newTabWriter(a.Stdout)
		fmt.Fprintln(tw, "SERVICE\tUSER\tTERMINAL\tPID\tIDLE")
		for _, s := range inventory.SSH {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Service, s.User, s.Terminal, sessionsPID(s.PID), sessionsSeconds(s.IdleSeconds, s.IdleSeconds > 0))
		}
		_ = tw.Flush()
	}
	if len(inventory.Terminal) > 0 {
		section("Terminal sessions:")
		tw := newTabWriter(a.Stdout)
		fmt.Fprintln(tw, "SERVICE\tCHECK\tTYPE\tNAME\tUSER\tSTATE\tWINDOWS\tIDLE")
		for _, s := range inventory.Terminal {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", s.Service, s.Check, s.Multiplexer, s.Name, s.User,
				sessionsCell(s.State), s.Windows, sessionsSeconds(s.IdleSeconds, s.HasIdle))
		}
		_ = tw.Flush()
	}
	if len(inventory.Database) > 0 {
		section("Database statements:")
		tw := newTabWriter(a.Stdout)
		fmt.Fprintln(tw, "SERVICE\tWATCH\tENGINE\tID\tUSER\tDB\tRUNNING\tLONG\tQUERY")
		for _, q := range inventory.Database {
			long := sessionsEmptyCell
			switch {
			case q.Stopping:
				long = sessionsStoppingMarker
			case q.Long:
				long = sessionsLongMarker
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n", sessionsCell(q.Service), q.Watch, q.Engine, q.ID,
				sessionsCell(q.User), sessionsCell(q.Database), sessionsSeconds(q.ElapsedSeconds, true), long,
				sessionsQueryCell(q.Query, q.Truncated))
		}
		_ = tw.Flush()
	}
	// A source that is collecting or unavailable is not an empty list: say so.
	for _, source := range inventory.Sources {
		if source.State == web.SessionSourceAvailable {
			continue
		}
		name := source.Service
		if source.Check != "" && name != "" {
			name += ":" + source.Check
		} else if source.Check != "" {
			name = source.Check
		}
		line := fmt.Sprintf("%s %s: %s", source.Kind, name, source.State)
		if source.Message != "" {
			line += " — " + source.Message
		}
		fmt.Fprintln(a.Stderr, line)
	}
	if !printed {
		fmt.Fprintln(a.Stdout, "no sessions or running statements")
	}
}

func sessionsCell(value string) string {
	if value == "" {
		return sessionsEmptyCell
	}
	return value
}

func sessionsPID(pid int) string {
	if pid <= 0 {
		return sessionsEmptyCell
	}
	return strconv.Itoa(pid)
}

func sessionsSeconds(seconds int64, known bool) string {
	if !known {
		return sessionsEmptyCell
	}
	return units.HumanizeDuration(time.Duration(seconds) * time.Second)
}

// sessionsQueryCell keeps a statement to one column width. The daemon already
// collapses whitespace and redacts credentials.
func sessionsQueryCell(query string, truncated bool) string {
	runes := []rune(query)
	if len(runes) > sessionsQueryColumnWidth {
		return string(runes[:sessionsQueryColumnWidth]) + sessionsTruncationEllipsis
	}
	if truncated {
		return string(runes) + sessionsTruncationEllipsis
	}
	return string(runes)
}
