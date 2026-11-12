package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"sermo/internal/config"
	"sermo/internal/operation"
	"sermo/internal/severity"
	"sermo/internal/state"
	"sermo/internal/web"
)

const (
	eventKindRecovered = "recovered"
	eventKindDryRun    = "dry-run"
	eventKindFiring    = "firing"
	eventKindAlert     = "alert"
	eventKindError     = "error"
)

// runEvents dispatches the events subcommands.
// - `sermoctl events [SERVICE] [--limit N]` lists recent events (global or for a service) via the daemon's web API.
// - `sermoctl events clear [--before TIME]` clears all events or events before a given time.
func (a App) runEvents(ctx context.Context, opts options) int {
	args := opts.args
	if len(args) > 0 && args[0] == commandArgClear {
		if len(args) > 1 {
			return a.commandUsageError(commandEvents, "events clear accepts only optional --before TIME")
		}
		return a.runEventsClear(ctx, opts, commandEvents)
	}
	if len(args) > 1 {
		return a.commandUsageError(commandEvents, "events accepts at most one service name")
	}

	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	service, limit := eventListTarget(opts, cfg)
	evs, err := a.FetchEvents(ctx, cfg, service, limit)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	a.writeEvents(opts, service, evs)
	return exitSuccess
}

// eventListTarget uses the command's configuration to canonicalize its filter.
func eventListTarget(opts options, cfg *config.Config) (string, int) {
	limit := defaultEventsListLimit
	if opts.eventLimit > 0 {
		limit = opts.eventLimit
	}
	if len(opts.args) == 0 {
		return "", limit
	}
	return canonicalServiceIfKnown(cfg, opts.args[0]), limit
}

func (a App) writeEvents(opts options, service string, evs []web.Event) {
	if opts.json {
		writeJSON(a.Stdout, evs)
		return
	}

	if len(evs) == 0 {
		if service != "" {
			fmt.Fprintf(a.Stdout, "no recent events for %s\n", service)
		} else {
			fmt.Fprintln(a.Stdout, "no recent events")
		}
		return
	}
	a.writeEventsTable(evs)
}

func (a App) writeEventsTable(evs []web.Event) {
	tw := newTabWriter(a.Stdout)
	fmt.Fprintln(tw, "TIME\tTARGET\tEVENT\tSEVERITY\tRULE\tMESSAGE")
	for _, e := range evs {
		r := eventTableFields(e)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.timestamp, r.target, r.kind, r.severity, r.rule, r.message)
	}
	_ = tw.Flush()
}

// eventTableRow is one rendered row of the events table, in column order.
type eventTableRow struct {
	timestamp string
	target    string
	kind      string
	severity  string
	rule      string
	message   string
}

func eventTableFields(e web.Event) eventTableRow {
	timestamp := eventTableValue(e.Time, eventsTableTimestampWidth)

	// The event's identity dimension is owned by the daemon's event model.
	target := e.Target()
	if target == "" {
		target = "-"
	}
	target = eventTableValue(target, eventsTableTargetWidth)

	level := eventTableValue(e.Severity, eventsTableSeverityWidth)
	if level == "" || e.Kind == eventKindRecovered || e.Kind == eventKindDryRun || e.Action != "" {
		level = "-"
	}
	// The rule distinguishes several rules of one service transitioning in the
	// same cycle, which otherwise render as identical rows.
	rule := e.Rule
	if rule == "" {
		rule = "-"
	}
	message := eventTableMessage(e.Message)
	if detail := eventDetail(e); detail != "" {
		message = detail + " · " + message
	}
	return eventTableRow{
		timestamp: timestamp,
		target:    target,
		kind:      eventLabel(e),
		severity:  level,
		rule:      rule,
		message:   message,
	}
}

// A recovery's grade belongs to the closed incident. Keep it in context, while
// only an explicit operation outcome can announce a completed action.
func eventLabel(e web.Event) string {
	action := e.Action
	if action == "" {
		action = e.Kind
	}
	if action == "" {
		action = "event"
	}
	action = eventWords(action)
	blocked := e.Status == string(operation.ResultBlocked) || e.Kind == "suppressed" || e.Kind == "panic-suppressed"
	switch {
	case e.Kind == eventKindRecovered:
		return "Recovered"
	case e.Kind == eventKindDryRun && e.Action != "":
		return action + " · Dry run"
	case e.Kind == eventKindDryRun:
		return "Dry run"
	case e.Status == string(operation.ResultOK):
		return action + " completed"
	case blocked && e.Action != "":
		return action + " blocked"
	case blocked:
		return "Blocked"
	case e.Action != "" && (eventFailedStatus(e.Status) || e.Kind == eventKindError || strings.HasSuffix(e.Kind, "-failed")):
		return action + " failed"
	case e.Status == "running":
		return action + " running"
	case e.Kind == eventKindAlert || e.Kind == eventKindFiring || e.Kind == eventKindError:
		return eventWords(severity.Level(e.Severity).Resolved().String())
	case e.Status != "":
		return action + " · " + e.Status
	default:
		return action
	}
}

func eventFailedStatus(status string) bool {
	switch status {
	case string(operation.ResultFailed), eventKindError, string(operation.ResultPreflightFailed),
		string(operation.ResultPostflightFailed), string(operation.ResultOrphanProcesses):
		return true
	default:
		return false
	}
}

func eventDetail(e web.Event) string {
	var parts []string
	if e.Kind == eventKindRecovered && e.Rule != "" {
		parts = append(parts, "Rule condition cleared")
	}
	if severity.Level(e.Severity).Valid() && (e.Kind == eventKindRecovered || e.Kind != eventKindAlert && e.Kind != eventKindFiring && e.Kind != eventKindError) {
		label := "Incident severity: "
		if e.Kind == eventKindRecovered {
			label = "Previous incident severity: "
		}
		parts = append(parts, label+e.Severity)
	}
	if eventFailedStatus(e.Status) && e.Status != string(operation.ResultFailed) {
		parts = append(parts, e.Status)
	}
	return strings.Join(parts, " · ")
}

func eventWords(value string) string {
	words := []rune(strings.ReplaceAll(strings.ReplaceAll(value, "-", " "), "_", " "))
	if len(words) != 0 {
		words[0] = unicode.ToUpper(words[0])
	}
	return string(words)
}

func eventTableValue(value string, width int) string {
	if utf8.RuneCountInString(value) > width {
		return string([]rune(value)[:width])
	}
	return value
}

func eventTableMessage(message string) string {
	// The message column is capped for terminal readability; tabwriter sizes
	// the rest to content.
	if utf8.RuneCountInString(message) > eventsTableMessageWidth {
		return eventTableValue(message, eventsTableMessageWidth-eventsTableEllipsisWidth) + eventsTableEllipsis
	}
	return message
}

// runActivity dispatches activity subcommands. Activity is the dashboard's
// recent-events view, so clearing it uses the same daemon event-prune path.
func (a App) runActivity(ctx context.Context, opts options) int {
	if len(opts.args) > 0 && opts.args[0] == commandArgClear {
		if len(opts.args) > 1 {
			return a.commandUsageError(commandActivity, "activity clear accepts only optional --before TIME")
		}
		return a.runEventsClear(ctx, opts, "activity entries")
	}
	return a.commandUsageError(commandActivity, "activity supports only: clear [--before TIME]")
}

func (a App) runEventsClear(ctx context.Context, opts options, noun string) int {
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}
	before, err := state.ParseCutoff(beforeFlagLabel, opts.before, time.Now())
	if err != nil {
		return a.fail(opts, err.Error())
	}
	n, err := a.PruneEvents(ctx, cfg, before)
	if err != nil {
		a.recordAccess(cfg, accessCommandEventsClear, "", accessStatusError, err.Error())
		return a.fail(opts, err.Error())
	}
	switch {
	case opts.json:
		writeJSON(a.Stdout, map[string]any{cliJSONKeyPruned: n})
	case before.IsZero():
		fmt.Fprintf(a.Stdout, "cleared %d %s\n", n, noun)
	default:
		fmt.Fprintf(a.Stdout, "cleared %d %s before %s\n", n, noun, before.Format(time.RFC3339))
	}
	a.recordAccess(cfg, accessCommandEventsClear, "", accessStatusOK, fmt.Sprintf("pruned %d %s", n, noun))
	return exitSuccess
}

func (a App) pruneDaemonEvents(ctx context.Context, cfg *config.Config, before time.Time) (int, error) {
	resp, err := a.daemonWebDo(ctx, cfg, http.MethodPost, "clear events", true, func(base string) string {
		u := base + web.APIPathEventsClear
		if !before.IsZero() {
			// Encoded and in UTC: a local offset such as +02:00 would otherwise
			// reach the daemon's query decoder with its "+" read as a space.
			u += "?" + url.Values{web.APIQueryBefore: {before.UTC().Format(time.RFC3339)}}.Encode()
		}
		return u
	})
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("clear failed (%d): %s%s", resp.StatusCode, strings.TrimSpace(string(body)), daemonWebStatusHint(resp.StatusCode))
	}

	var res struct {
		OK     bool `json:"ok"`
		Pruned int  `json:"pruned"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		// some responses may be plain
		return 0, fmt.Errorf("unexpected response: %s", body)
	}
	return res.Pruned, nil
}

// fetchEvents (the default for App.FetchEvents) calls the daemon web API to retrieve
// recent events. If service != "", uses the per-service endpoint.
func (a App) fetchEvents(ctx context.Context, cfg *config.Config, service string, limit int) ([]web.Event, error) {
	// no CSRF needed for GET; auth is attached when configured
	resp, err := a.daemonWebDo(ctx, cfg, http.MethodGet, "events", false, func(base string) string {
		if service != "" {
			return fmt.Sprintf("%s%s/%s%s?%s=%d", base, web.APIPathServices, url.PathEscape(service), web.APIPathServiceEvents, web.APIQueryLimit, limit)
		}
		return fmt.Sprintf("%s%s?%s=%d", base, web.APIPathEvents, web.APIQueryLimit, limit)
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("events fetch failed (%d): %s%s", resp.StatusCode, strings.TrimSpace(string(body)), daemonWebStatusHint(resp.StatusCode))
	}

	if service != "" {
		var events []web.Event
		if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
			return nil, fmt.Errorf("decode service events: %w", err)
		}
		return events, nil
	}

	var page web.EventPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}
	return page.Events, nil
}
