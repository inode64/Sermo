package app

import (
	"context"
	"slices"
	"time"

	"sermo/internal/config"
	"sermo/internal/rules"
	"sermo/internal/web"
)

const (
	// activitySummaryEventScanLimit bounds the recent event scan used for the
	// dashboard rollup; event list endpoints keep their own request limits.
	activitySummaryEventScanLimit = 500
	webEventPageScanSize          = 500
	webEventPageMaxScan           = 5000
)

func serviceOperationActionList() []string {
	return []string{
		string(rules.ActionStart),
		string(rules.ActionStop),
		string(rules.ActionRestart),
		string(rules.ActionReload),
		string(rules.ActionResume),
	}
}

// ActivitySummary returns a rollup of recent events for the dashboard.
func (b *WebBackend) ActivitySummary(_ context.Context) web.ActivitySummary {
	summary := web.ActivitySummary{}
	if b.events == nil {
		return summary
	}

	events := b.events.Recent("", activitySummaryEventScanLimit)
	if len(events) > 0 {
		summary.LastEventKind = events[0].Kind
	}
	for i := range events {
		switch {
		case events[i].Kind == eventKindCascade && rules.ActionType(events[i].Action).IsOperation():
			// Cascade keeps one kind for every outcome (the primary encodes
			// failures via eventKindForResult), so gate on status here: blocked
			// stays uncounted like suppressed primaries, any failure is an error.
			if events[i].Status != eventStatusOK && events[i].Status != eventStatusBlocked {
				summary.Errors++
			}
		case events[i].Kind == eventKindError:
			summary.Errors++
		}
	}
	return summary
}

// Events returns the most recent events, newest first.
func (b *WebBackend) Events(_ context.Context, limit int) []web.Event {
	if b.events == nil {
		return nil
	}
	return toWebEvents(b.events.Recent("", limit))
}

// EventPage returns one stable, filtered cursor page from the persisted event
// feed. It scans bounded raw batches so selective filters can still fill a page.
func (b *WebBackend) EventPage(_ context.Context, query web.EventQuery) web.EventPage {
	if b.events == nil || query.Limit <= 0 {
		return web.EventPage{}
	}
	scan := newWebEventPageScan(query, b.eventPageCutoff(query))
	for {
		batch := b.events.Page(scan.cursor, webEventPageScanSize+1)
		if len(batch) == 0 {
			return scan.page(false)
		}
		batch, hasRawMore := trimWebEventPageBatch(batch)
		if page, complete := scan.addBatch(batch, hasRawMore); complete {
			return page
		}
		if scan.scanned >= webEventPageMaxScan && hasRawMore {
			return scan.page(true)
		}
		if !hasRawMore {
			return scan.page(false)
		}
	}
}

func (b *WebBackend) eventPageCutoff(query web.EventQuery) time.Time {
	if query.Since <= 0 {
		return time.Time{}
	}
	return b.webNow().Add(-query.Since)
}

func trimWebEventPageBatch(batch []LoggedEvent) ([]LoggedEvent, bool) {
	if len(batch) <= webEventPageScanSize {
		return batch, false
	}
	return batch[:webEventPageScanSize], true
}

type webEventPageScan struct {
	query   web.EventQuery
	cutoff  time.Time
	events  []web.Event
	cursor  int64
	scanned int
}

func newWebEventPageScan(query web.EventQuery, cutoff time.Time) webEventPageScan {
	return webEventPageScan{
		query:  query,
		cutoff: cutoff,
		events: make([]web.Event, 0, query.Limit),
		cursor: query.BeforeID,
	}
}

func (scan *webEventPageScan) addBatch(batch []LoggedEvent, hasRawMore bool) (web.EventPage, bool) {
	for i := range batch {
		scan.scanned++
		scan.cursor = batch[i].ID
		if !scan.cutoff.IsZero() && batch[i].Time.Before(scan.cutoff) {
			continue
		}
		event := loggedEventToWeb(batch[i])
		if !webEventMatchesQuery(event, scan.query) {
			continue
		}
		scan.events = append(scan.events, event)
		if len(scan.events) >= scan.query.Limit {
			return scan.page(i < len(batch)-1 || hasRawMore), true
		}
	}
	return web.EventPage{}, false
}

func (scan *webEventPageScan) page(hasMore bool) web.EventPage {
	page := web.EventPage{Events: scan.events, HasMore: hasMore}
	if hasMore {
		page.NextBeforeID = scan.cursor
	}
	return page
}

func webEventMatchesQuery(event web.Event, query web.EventQuery) bool {
	if query.Service != "" && event.Service != query.Service ||
		query.Watch != "" && event.Watch != query.Watch ||
		query.Kind != "" && event.Kind != query.Kind ||
		query.Status != "" && event.Status != query.Status {
		return false
	}
	return !query.OnlyErrors || web.IsErrorEvent(event)
}

// ServiceEvents returns one service's recent events.
func (b *WebBackend) ServiceEvents(_ context.Context, name string, limit int) ([]web.Event, bool) {
	if _, ok := b.entries[name]; !ok {
		return nil, false
	}
	if b.events == nil {
		return nil, true
	}
	return toWebEvents(b.events.Recent(name, limit)), true
}

// ApplicationEvents returns one application's recent monitoring events
// (firing/recovered/notify on the App dimension); ok is false for unknown apps.
func (b *WebBackend) ApplicationEvents(_ context.Context, name string, limit int) ([]web.Event, bool) {
	if !b.knownApp(name) {
		return nil, false
	}
	if b.events == nil {
		return nil, true
	}
	return toWebEvents(b.events.RecentApp(name, limit)), true
}

func (b *WebBackend) knownApp(name string) bool {
	if name == "" || b.cfg == nil {
		return false
	}
	return slices.Contains(b.cfg.CatalogNamesInCategory(config.CategoryApp), name)
}

// PruneEvents removes events older than before (all if zero) from the live log.
func (b *WebBackend) PruneEvents(_ context.Context, before time.Time) int {
	if b.events == nil {
		return 0
	}
	return b.events.Prune(before)
}

func toWebEvents(events []LoggedEvent) []web.Event {
	out := make([]web.Event, 0, len(events))
	for i := range events {
		out = append(out, loggedEventToWeb(events[i]))
	}
	return out
}

func loggedEventToWeb(event LoggedEvent) web.Event {
	// Events restored from the store carry UTC times while fresh ones carry the
	// local zone; normalizing here keeps one timestamp convention (UTC) across
	// restarts in the web UI, sermoctl and notifications.
	return web.Event{
		ID:      event.ID,
		Time:    event.Time.UTC().Format(time.RFC3339),
		Service: event.Service,
		Watch:   event.Watch,
		App:     event.App,
		Kind:    event.Kind,
		Rule:    event.Rule,
		Action:  event.Action,
		Status:  event.Status,
		Message: event.Message,
		Output:  event.Output,
	}
}

func (b *WebBackend) lastServiceEvent(name string) *web.Event {
	if b.events == nil {
		return nil
	}
	event, ok := b.events.LastService(name)
	if !ok {
		return nil
	}
	webEvent := loggedEventToWeb(event)
	return &webEvent
}
