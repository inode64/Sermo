package app

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"sermo/internal/config"
	"sermo/internal/web"
)

// Manual refresh subjects name the panel a refresh covers in its result and
// its event.
const (
	refreshSubjectApplications = "applications"
	refreshSubjectLibraries    = "libraries"
	refreshSubjectWatches      = "watches"
)

// refreshListedNames bounds how many watch names one refresh outcome lists per
// group; the rest collapse into a "+N more" entry.
const refreshListedNames = 5

// RefreshApplications runs every installed application's watch now, besides its
// artifact interval; the next listing rebuilds from the new samples.
func (b *WebBackend) RefreshApplications(ctx context.Context) web.ActionResult {
	return b.refreshCatalog(ctx, refreshSubjectApplications, config.CategoryApp, &b.applications)
}

// RefreshLibraries runs every installed library's watch now; the next listing
// rebuilds, probing the libraries itself.
func (b *WebBackend) RefreshLibraries(ctx context.Context) web.ActionResult {
	return b.refreshCatalog(ctx, refreshSubjectLibraries, config.CategoryLibrary, &b.libraries)
}

func (b *WebBackend) refreshCatalog(ctx context.Context, subject, category string, inventory *catalogInventoryCache) web.ActionResult {
	ctx, cancel := b.operationContext(ctx, 0)
	defer cancel()
	outcome := b.watchSchedule.Refresh(ctx, func(w *Watch) bool { return w.CheckType == category })
	// Invalidate after the cycles stored their samples: the next listing
	// rebuilds from them, and a scan started before the refresh cannot cache
	// older ones.
	inventory.invalidate()
	return b.refreshResult("", subject, outcome, nil)
}

// RefreshWatches runs an extra cycle of the named watches now, or of every
// enabled watch when names is empty. Each runs its own scheduled cycle, so
// its rules, notifications and actions apply as on any other cycle.
func (b *WebBackend) RefreshWatches(ctx context.Context, names []string) web.ActionResult {
	selected := make(map[string]string, len(b.watches))
	for _, name := range names {
		w := b.watches[name]
		if w == nil {
			return web.ActionResult{Message: fmt.Sprintf(unknownWatchMessageFmt, name)}
		}
		if w.disabled {
			return web.ActionResult{Message: fmt.Sprintf("watch %q is disabled", name)}
		}
		selected[name] = w.checkType
	}
	if len(names) == 0 {
		for name, w := range b.watches {
			if !w.disabled {
				selected[name] = w.checkType
			}
		}
	}
	ctx, cancel := b.operationContext(ctx, 0)
	defer cancel()
	outcome := b.watchSchedule.Refresh(ctx, func(w *Watch) bool {
		// A row is its runtime's name and check type, the key its readings are
		// published under; an application watch sharing the name is another row.
		checkType, ok := selected[w.Name]
		return ok && w.CheckType == checkType
	})
	// A named watch with no running cycle is reported, not silently counted:
	// every watch before engine.startup_delay has ended, and the watches of a
	// service whose unit the daemon could not resolve.
	found := slices.Concat(outcome.Refreshed, outcome.Paused, outcome.Pending)
	unscheduled := slices.DeleteFunc(slices.Clone(names), func(name string) bool { return slices.Contains(found, name) })
	watch := ""
	if len(names) == 1 {
		watch = names[0]
	}
	return b.refreshResult(watch, refreshSubjectWatches, outcome, unscheduled)
}

// refreshResult reports one refresh and records it as one auditable event: a
// refreshed cycle can run a watch's actions earlier than its schedule would
// have. The event stays an action even when the refresh did not complete: a
// slow or early refresh is not an incident, and an error event would reach
// event_notify's operators.
func (b *WebBackend) refreshResult(watch, what string, outcome WatchRefreshOutcome, unscheduled []string) web.ActionResult {
	parts := []string{fmt.Sprintf("%s refreshed: %d", what, len(outcome.Refreshed))}
	if outcome.NotRunning {
		parts = append(parts, "watches are not scheduled yet (engine.startup_delay or a reload in progress)")
	}
	parts = appendRefreshGroup(parts, "not monitored", outcome.Paused)
	parts = appendRefreshGroup(parts, "not refreshed by the deadline", outcome.Pending)
	parts = appendRefreshGroup(parts, "not scheduled", unscheduled)
	message := strings.Join(parts, "; ")
	ok := !outcome.NotRunning && len(outcome.Pending) == 0 && len(unscheduled) == 0
	status := eventStatusOK
	if !ok {
		status = eventStatusFailed
	}
	b.emitWatchMonitorEvent(watch, eventActionRefresh, eventKindAction, status, message)
	return web.ActionResult{OK: ok, Message: message}
}

func appendRefreshGroup(parts []string, label string, names []string) []string {
	if len(names) == 0 {
		return parts
	}
	list := limitedDisplayList(names, refreshListedNames, func(name string) string { return name })
	return append(parts, fmt.Sprintf("%s: %d (%s)", label, len(names), list))
}
