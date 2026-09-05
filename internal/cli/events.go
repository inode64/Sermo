package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sermo/internal/state"
	"sermo/internal/web"
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

	service, limit := a.eventListTarget(opts)
	evs, err := a.FetchEvents(ctx, opts, service, limit)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	a.writeEvents(opts, service, evs)
	return exitSuccess
}

// eventListTarget returns the service filter and limit for `sermoctl events`.
// Config loading is best effort so the daemon can still serve events when the
// local configuration is unavailable.
func (a App) eventListTarget(opts options) (string, int) {
	limit := defaultEventsListLimit
	if opts.eventLimit > 0 {
		limit = opts.eventLimit
	}
	if len(opts.args) == 0 {
		return "", limit
	}

	service := opts.args[0]
	if a.LoadConfig == nil {
		return service, limit
	}
	if cfg, err := a.LoadConfig(opts.globalPath()); err == nil {
		service = canonicalServiceIfKnown(cfg, service)
	}
	return service, limit
}

func (a App) writeEvents(opts options, service string, evs []event) {
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

func (a App) writeEventsTable(evs []event) {
	tw := newTabWriter(a.Stdout)
	fmt.Fprintln(tw, "TIME\tTARGET\tKIND\tRULE\tACTION\tMESSAGE")
	for _, e := range evs {
		r := eventTableFields(e)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.timestamp, r.target, r.kind, r.rule, r.action, r.message)
	}
	_ = tw.Flush()
}

// eventTableRow is one rendered row of the events table, in column order.
type eventTableRow struct {
	timestamp string
	target    string
	kind      string
	rule      string
	action    string
	message   string
}

func eventTableFields(e event) eventTableRow {
	timestamp := e.Time
	if len(timestamp) >= eventsTableTimestampWidth {
		timestamp = timestamp[:eventsTableTimestampWidth]
	}

	// The event's identity dimension is owned by the daemon's event model.
	target := e.Target()
	if target == "" {
		target = "-"
	}
	target = eventTableValue(target, eventsTableTargetWidth)

	kind := eventTableValue(e.Kind, eventsTableKindWidth)
	// The rule distinguishes several rules of one service transitioning in the
	// same cycle, which otherwise render as identical rows.
	rule := eventTableValue(e.Rule, eventsTableRuleWidth)
	if rule == "" {
		rule = "-"
	}
	action := e.Action
	if action == "" {
		action = e.Status
	}
	action = eventTableValue(action, eventsTableActionWidth)
	return eventTableRow{
		timestamp: timestamp,
		target:    target,
		kind:      kind,
		rule:      rule,
		action:    action,
		message:   eventTableMessage(e.Message),
	}
}

func eventTableValue(value string, width int) string {
	if len(value) > width {
		return value[:width]
	}
	return value
}

func eventTableMessage(message string) string {
	// The message column is capped for terminal readability; tabwriter sizes
	// the rest to content.
	if len(message) > eventsTableMessageWidth {
		return message[:eventsTableMessageWidth-eventsTableEllipsisWidth] + eventsTableEllipsis
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
	before, err := parseBefore(opts.before, time.Now)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	pruneEvents := a.PruneEvents
	if pruneEvents == nil {
		pruneEvents = a.pruneDaemonEvents
	}
	n, err := pruneEvents(ctx, opts, before)
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

// parseBefore reads the shared --before cutoff through its owner in the state
// package, which also consumes it in PruneEvents and CompactHistory.
func parseBefore(value string, now func() time.Time) (time.Time, error) {
	//nolint:wrapcheck // ParseCutoff already names --before and states the accepted forms; the message is printed verbatim as the usage error.
	return state.ParseCutoff(beforeFlagLabel, value, now())
}

func (a App) pruneDaemonEvents(ctx context.Context, opts options, before time.Time) (int, error) {
	resp, err := a.daemonWebRequest(ctx, opts, http.MethodPost, "clear events", true, func(base string) string {
		u := base + web.APIPathEventsClear
		if !before.IsZero() {
			u += "?" + web.APIQueryBefore + "=" + before.Format(time.RFC3339)
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
func (a App) fetchEvents(ctx context.Context, opts options, service string, limit int) ([]event, error) {
	// no CSRF needed for GET; auth is attached when configured
	resp, err := a.daemonWebRequest(ctx, opts, http.MethodGet, "events", false, func(base string) string {
		if service != "" {
			return fmt.Sprintf("%s%s/%s%s?%s=%d", base, web.APIPathServices, service, web.APIPathServiceEvents, web.APIQueryLimit, limit)
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
		var events []event
		if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
			return nil, fmt.Errorf("decode service events: %w", err)
		}
		return events, nil
	}

	var page eventPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}
	return page.Events, nil
}
