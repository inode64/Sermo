package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/servicemgr"
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
	holder := &WebBackendHolder{b: old, generation: initialWebBackendGeneration}

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
	holder.mu.Lock()
	holder.b = next
	holder.generation++
	holder.mu.Unlock()
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
