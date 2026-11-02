package app

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/web"
)

func refreshTestBackend(refresh *WatchSchedule, watches map[string]*webWatch, events *[]Event) *WebBackend {
	return &WebBackend{
		watches:       watches,
		watchSchedule: refresh,
		emit:          func(e Event) { *events = append(*events, e) },
	}
}

func TestWebBackendRefreshWatchesRefusesUnknownAndDisabledNames(t *testing.T) {
	var runs atomic.Int32
	refresh, _ := startRefreshScheduler(t, countingWatch("disk", &runs))
	waitRuns(t, &runs, 1)
	var events []Event
	b := refreshTestBackend(refresh, map[string]*webWatch{
		"disk": {name: "disk"},
		"off":  {name: "off", disabled: true},
	}, &events)

	for name, want := range map[string]string{"typo": `unknown watch "typo"`, "off": `watch "off" is disabled`} {
		res := b.RefreshWatches(t.Context(), []string{"disk", name})
		if res.OK || res.Message != want {
			t.Fatalf("RefreshWatches(disk, %s) = %+v, want refusal %q", name, res, want)
		}
	}
	if runs.Load() != 1 || len(events) != 0 {
		t.Fatalf("a refused refresh ran %d cycles and emitted %+v", runs.Load()-1, events)
	}
}

// An application watch is named after its application, which may be the name
// of a configured watch too: a row is its runtime's name and check type.
func TestWebBackendRefreshWatchesRunsConfiguredWatchesOnly(t *testing.T) {
	var diskRuns, appRuns atomic.Int32
	app := countingWatch("disk", &appRuns)
	app.CheckType = config.CategoryApp
	disk := countingWatch("disk", &diskRuns)
	disk.CheckType = "disk"
	refresh, _ := startRefreshScheduler(t, disk, app)
	waitRuns(t, &diskRuns, 1)
	waitRuns(t, &appRuns, 1)
	var events []Event
	b := refreshTestBackend(refresh, map[string]*webWatch{
		"disk": {name: "disk", checkType: "disk"},
		"off":  {name: "off", checkType: "disk", disabled: true},
	}, &events)

	res := b.RefreshWatches(t.Context(), nil)
	if !res.OK || res.Message != "watches refreshed: 1" {
		t.Fatalf("RefreshWatches(all) = %+v", res)
	}
	if diskRuns.Load() != 2 || appRuns.Load() != 1 {
		t.Fatalf("runs disk=%d app=%d, want only the configured watch cycled", diskRuns.Load(), appRuns.Load())
	}
	if len(events) != 1 || events[0].Action != eventActionRefresh || events[0].Kind != eventKindAction ||
		events[0].Status != eventStatusOK || events[0].Watch != "" || events[0].Message != "watches refreshed: 1" {
		t.Fatalf("events = %+v, want one auditable refresh event", events)
	}
}

// A named watch the running scheduler has no cycle for (a service whose unit
// the daemon could not resolve) is reported, not silently counted. The event
// stays an action: an incomplete refresh is not an incident to notify.
func TestWebBackendRefreshWatchesReportsAnUnscheduledName(t *testing.T) {
	var runs atomic.Int32
	refresh, _ := startRefreshScheduler(t, countingWatch("load", &runs))
	waitRuns(t, &runs, 1)
	var events []Event
	b := refreshTestBackend(refresh, map[string]*webWatch{"svc:queue": {name: "svc:queue", checkType: "queue", serviceScoped: true}}, &events)

	res := b.RefreshWatches(t.Context(), []string{"svc:queue"})
	if res.OK || res.Message != "watches refreshed: 0; not scheduled: 1 (svc:queue)" {
		t.Fatalf("RefreshWatches(svc:queue) = %+v", res)
	}
	if len(events) != 1 || events[0].Watch != "svc:queue" || events[0].Kind != eventKindAction || events[0].Status != eventStatusFailed {
		t.Fatalf("events = %+v, want a failed refresh action on the watch", events)
	}
	if eventNeedsNotification(events[0]) {
		t.Fatalf("event %+v would be notified as an incident", events[0])
	}
}

// Before the scheduler has started its watches a refresh runs nothing, and
// says so instead of answering a clean "refreshed: 0".
func TestWebBackendRefreshBeforeWatchesAreScheduledFails(t *testing.T) {
	var events []Event
	b := refreshTestBackend(NewWatchSchedule(), map[string]*webWatch{"disk": {name: "disk", checkType: "disk"}}, &events)
	inventory := catalogInventoryCache{}
	for name, res := range map[string]web.ActionResult{
		"watches": b.RefreshWatches(t.Context(), nil),
		"apps":    b.refreshCatalog(t.Context(), refreshSubjectApplications, config.CategoryApp, &inventory),
	} {
		if res.OK || !strings.Contains(res.Message, "not scheduled yet") {
			t.Fatalf("%s refresh = %+v, want a failure naming the unscheduled watches", name, res)
		}
	}
}

func TestRefreshResultListsAtMostFiveNamesPerGroup(t *testing.T) {
	var events []Event
	b := refreshTestBackend(nil, nil, &events)
	res := b.refreshResult("", refreshSubjectWatches, WatchRefreshOutcome{
		Refreshed: []string{"a"},
		Paused:    []string{"p"},
		Pending:   []string{"b", "c", "d", "e", "f", "g", "h"},
	}, nil)
	want := "watches refreshed: 1; not monitored: 1 (p); not refreshed by the deadline: 7 (b, c, d, e, f, +2 more)"
	if res.OK || res.Message != want {
		t.Fatalf("result = %+v, want %q", res, want)
	}
}

func TestWebBackendRefreshCatalogCyclesItsWatchesThenRebuildsTheList(t *testing.T) {
	var libRuns, hostRuns atomic.Int32
	lib := countingWatch(libraryWatchNamePrefix+"openssl", &libRuns)
	lib.CheckType = config.CategoryLibrary
	host := countingWatch("load", &hostRuns)
	refresh, _ := startRefreshScheduler(t, lib, host)
	waitRuns(t, &libRuns, 1)
	waitRuns(t, &hostRuns, 1)
	var events []Event
	b := refreshTestBackend(refresh, nil, &events)
	stale := []web.CatalogItem{{Name: "openssl", Version: "3.0"}}
	inventory := catalogInventoryCache{at: time.Now(), items: stale}
	var loads atomic.Int32
	load := func(context.Context) []web.CatalogItem {
		loads.Add(1)
		// The next listing must read the samples the refreshed cycle stored.
		if libRuns.Load() != 2 {
			t.Errorf("inventory rebuilt before the library watch cycled (runs=%d)", libRuns.Load())
		}
		return []web.CatalogItem{{Name: "openssl", Version: "3.5"}}
	}

	res := b.refreshCatalog(t.Context(), refreshSubjectLibraries, config.CategoryLibrary, &inventory)
	if !res.OK || res.Message != "libraries refreshed: 1" {
		t.Fatalf("refresh = %+v", res)
	}
	if hostRuns.Load() != 1 || loads.Load() != 0 {
		t.Fatalf("a library refresh cycled a host watch or rebuilt the list itself (loads=%d)", loads.Load())
	}
	for range 2 {
		if got := b.catalogItems(t.Context(), &inventory, load); len(got) != 1 || got[0].Version != "3.5" {
			t.Fatalf("after refresh: items %+v, want the rebuilt list", got)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("listing after refresh rebuilt %d times, want once then served from cache", loads.Load())
	}
}

// A scan that started before a refresh read the samples from before it; its
// result must not be cached over the refreshed inventory.
func TestCatalogInventoryScanStartedBeforeInvalidateIsNotCached(t *testing.T) {
	b := &WebBackend{}
	var inventory catalogInventoryCache
	inScan := make(chan struct{})
	release := make(chan struct{})
	staleDone := make(chan []web.CatalogItem, 1)
	go func() {
		staleDone <- b.catalogItems(t.Context(), &inventory, func(context.Context) []web.CatalogItem {
			close(inScan)
			<-release
			return []web.CatalogItem{{Name: "nginx", Version: "old"}}
		})
	}()
	<-inScan
	inventory.invalidate()
	close(release)
	if got := <-staleDone; len(got) != 1 || got[0].Version != "old" {
		t.Fatalf("stale scan answered %+v to its own caller", got)
	}

	got := b.catalogItems(t.Context(), &inventory, func(context.Context) []web.CatalogItem {
		return []web.CatalogItem{{Name: "nginx", Version: "new"}}
	})
	if len(got) != 1 || got[0].Version != "new" {
		t.Fatalf("after invalidate = %+v, want a fresh scan instead of the stale one", got)
	}
	if len(inventory.items) != 1 || inventory.items[0].Version != "new" {
		t.Fatalf("cached = %+v, want the fresh scan", inventory.items)
	}
}

// After a refresh the inventory is expired, not emptied: while one viewer
// rebuilds it, every other viewer is still served the previous complete list.
func TestCatalogInventoryKeepsServingThePreviousListWhileARefreshRebuilds(t *testing.T) {
	b := &WebBackend{}
	inventory := catalogInventoryCache{at: time.Now(), items: []web.CatalogItem{{Name: "nginx", Version: "old"}}}
	inventory.invalidate()
	inScan := make(chan struct{})
	release := make(chan struct{})
	rebuilt := make(chan []web.CatalogItem, 1)
	go func() {
		rebuilt <- b.catalogItems(t.Context(), &inventory, func(context.Context) []web.CatalogItem {
			close(inScan)
			<-release
			return []web.CatalogItem{{Name: "nginx", Version: "new"}}
		})
	}()
	<-inScan
	got := b.catalogItems(t.Context(), &inventory, func(context.Context) []web.CatalogItem {
		t.Error("a second viewer rescanned instead of reading the previous list")
		return nil
	})
	close(release)
	if len(got) != 1 || got[0].Version != "old" {
		t.Fatalf("viewer during the rebuild got %+v, want the previous list", got)
	}
	if got := <-rebuilt; len(got) != 1 || got[0].Version != "new" {
		t.Fatalf("rebuild = %+v, want the new list", got)
	}
}

// The watch list carries the next scheduled check of a running, monitored
// watch; a disabled watch has none.
func TestWebBackendWatchesCarryTheNextCheck(t *testing.T) {
	var runs atomic.Int32
	w := countingWatch("disk", &runs)
	w.CheckType = "disk"
	schedule, _ := startRefreshScheduler(t, w)
	waitRuns(t, &runs, 1)
	var events []Event
	b := refreshTestBackend(schedule, map[string]*webWatch{
		"disk": {name: "disk", checkType: "disk", interval: refreshTestInterval},
		"off":  {name: "off", checkType: "disk", disabled: true},
	}, &events)
	b.watchOrder = []string{"disk", "off"}
	deadline := time.After(2 * time.Second)
	for {
		views := b.Watches(t.Context())
		if len(views) != 2 || views[1].NextCheckAt != "" {
			t.Fatalf("views = %+v, want a disabled watch without a next check", views)
		}
		if next, err := time.Parse(time.RFC3339, views[0].NextCheckAt); err == nil {
			if until := time.Until(next); until < refreshTestInterval-time.Minute || until > refreshTestInterval {
				t.Fatalf("next check in %s, want about %s", until, refreshTestInterval)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("watch %+v never carried its next check", views[0])
		case <-time.After(time.Millisecond):
		}
	}
}
