package app

import (
	"context"
	"strings"
	"time"

	"sermo/internal/config"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/web"
)

const (
	// activitySummaryEventScanLimit bounds the recent event scan used for the
	// dashboard rollup; event list endpoints keep their own request limits.
	activitySummaryEventScanLimit = 500
	webEventPageScanSize          = 500
	webEventPageMaxScan           = 5000
	webEventStatusError           = "error"
)

func isServiceOperationAction(action string) bool {
	switch rules.ActionType(action) {
	case rules.ActionStart, rules.ActionStop, rules.ActionRestart, rules.ActionReload, rules.ActionResume:
		return true
	default:
		return false
	}
}

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
	summary.TotalEvents = len(events)
	if len(events) > 0 {
		latest := events[0]
		summary.LastEventTime = latest.Time.Format(time.RFC3339)
		summary.LastEventKind = latest.Kind
		summary.LastEventService = latest.Service
		summary.LastEventWatch = latest.Watch
	}
	for _, event := range events {
		switch {
		case event.Kind == eventKindAction && isServiceOperationAction(event.Action):
			summary.ServiceActions++
		case event.Kind == eventKindHook || event.Kind == eventKindHookFail:
			summary.WatchHooks++
		case event.Kind == eventKindNotify || event.Kind == eventKindNotifyFail:
			summary.WatchNotifies++
		case event.Kind == eventKindError:
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
	out := make([]web.Event, 0, query.Limit)
	cursor := query.BeforeID
	now := time.Now
	if b.now != nil {
		now = b.now
	}
	cutoff := time.Time{}
	if query.Since > 0 {
		cutoff = now().Add(-query.Since)
	}
	scanned := 0
	for {
		batch := b.events.Page(cursor, webEventPageScanSize+1)
		if len(batch) == 0 {
			return web.EventPage{Events: out}
		}
		hasRawMore := len(batch) > webEventPageScanSize
		if hasRawMore {
			batch = batch[:webEventPageScanSize]
		}
		for i, logged := range batch {
			scanned++
			cursor = logged.ID
			if !cutoff.IsZero() && logged.Time.Before(cutoff) {
				continue
			}
			event := loggedEventToWeb(logged)
			if !webEventMatchesQuery(event, query) {
				continue
			}
			out = append(out, event)
			if len(out) >= query.Limit {
				hasMore := i < len(batch)-1 || hasRawMore
				page := web.EventPage{Events: out, HasMore: hasMore}
				if hasMore {
					page.NextBeforeID = cursor
				}
				return page
			}
		}
		if scanned >= webEventPageMaxScan && hasRawMore {
			return web.EventPage{Events: out, NextBeforeID: cursor, HasMore: true}
		}
		if !hasRawMore {
			return web.EventPage{Events: out}
		}
	}
}

func webEventMatchesQuery(event web.Event, query web.EventQuery) bool {
	if query.Service != "" && event.Service != query.Service ||
		query.Watch != "" && event.Watch != query.Watch ||
		query.Kind != "" && event.Kind != query.Kind ||
		query.Status != "" && event.Status != query.Status {
		return false
	}
	if !query.OnlyErrors {
		return true
	}
	if event.Kind == eventKindError || strings.Contains(event.Kind, string(operation.ResultFailed)) {
		return true
	}
	switch event.Status {
	case eventStatusFailed, webEventStatusError, string(operation.ResultBlocked),
		string(operation.ResultOrphanProcesses), string(operation.ResultPreflightFailed),
		string(operation.ResultPostflightFailed):
		return true
	default:
		return false
	}
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
	for _, candidate := range b.cfg.CatalogNamesInCategory(config.CategoryApp) {
		if candidate == name {
			return true
		}
	}
	return false
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
	for _, event := range events {
		out = append(out, loggedEventToWeb(event))
	}
	return out
}

func loggedEventToWeb(event LoggedEvent) web.Event {
	return web.Event{
		ID:      event.ID,
		Time:    event.Time.Format(time.RFC3339),
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
