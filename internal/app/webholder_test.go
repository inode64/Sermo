package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/servicemgr"
	"sermo/internal/web"
)

func TestWebBackendHolderDashboardSnapshotKeepsOneGeneration(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	old := dashboardSnapshotBackend("old", "old-notifier")
	old.entries["old"].status = func(context.Context) (servicemgr.Status, error) {
		close(started)
		<-release
		return servicemgr.StatusActive, nil
	}
	next := dashboardSnapshotBackend("new", "new-notifier")
	holder := &WebBackendHolder{}
	holder.current.Store(&webGeneration{backend: old, generation: initialWebBackendGeneration})

	done := make(chan struct{})
	var snapshot struct {
		service    string
		notifier   string
		generation uint64
	}
	go func() {
		got := holder.DashboardSnapshot(context.Background(), time.Hour)
		if len(got.Services) > 0 {
			snapshot.service = got.Services[0].Name
		}
		if len(got.Notifiers) > 0 {
			snapshot.notifier = got.Notifiers[0].Name
		}
		snapshot.generation = got.Generation
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("old dashboard generation did not start")
	}
	holder.current.Store(&webGeneration{backend: next, generation: initialWebBackendGeneration + 1})
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dashboard snapshot did not complete")
	}
	if snapshot.service != "old" || snapshot.notifier != "old-notifier" {
		t.Fatalf("dashboard snapshot = %+v, want one old generation", snapshot)
	}
	if snapshot.generation != initialWebBackendGeneration {
		t.Fatalf("dashboard generation = %d, want %d", snapshot.generation, initialWebBackendGeneration)
	}
}

// A web mutation keeps its pinned generation for the whole action (up to the
// operation timeout), so pinning must not make a reload — and, through the
// monitor lock it holds, the whole monitoring cycle — wait for that action.
func TestWebBackendHolderReloadIgnoresPinnedReads(t *testing.T) {
	watchCfg := func(name string) *config.Config {
		return cfgWithWatches(map[string]any{name: map[string]any{"check": map[string]any{"type": "count"}}})
	}
	holder, _ := NewWebBackendHolder(t.Context(), watchCfg("before-reload"), Deps{})

	pinned, generation := holder.BeginBackendRead()
	if pinned == nil || generation != initialWebBackendGeneration {
		t.Fatalf("pinned generation = %d, want %d", generation, initialWebBackendGeneration)
	}

	reloaded := make(chan struct{})
	go func() {
		holder.Reload(context.Background(), watchCfg("after-reload"), Deps{})
		close(reloaded)
	}()
	select {
	case <-reloaded:
	case <-time.After(5 * time.Second):
		t.Fatal("reload waited for an outstanding pinned read")
	}

	if _, current := holder.BeginBackendRead(); current != initialWebBackendGeneration+1 {
		t.Fatalf("generation after reload = %d, want %d", current, initialWebBackendGeneration+1)
	}
	if got := watchNames(holder.Watches(context.Background())); got != "after-reload" {
		t.Fatalf("holder watches = %q, want the reloaded generation", got)
	}
	// The pin is the instance: an action that passed its precondition keeps the
	// identity it was authorized against.
	if got := watchNames(pinned.Watches(context.Background())); got != "before-reload" {
		t.Fatalf("pinned watches = %q, want the pinned generation", got)
	}
}

func watchNames(watches []web.Watch) string {
	names := make([]string, 0, len(watches))
	for _, w := range watches {
		names = append(names, w.Name)
	}
	return strings.Join(names, ",")
}

func dashboardSnapshotBackend(service, notifier string) *WebBackend {
	return &WebBackend{
		cfg:   &config.Config{},
		order: []string{service},
		entries: map[string]*webEntry{
			service: {
				status: func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
			},
		},
		notifierOrder: []string{notifier},
		notifiers:     map[string]*webNotifier{notifier: {name: notifier}},
	}
}
