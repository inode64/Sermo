package app

import (
	"context"
	"testing"

	"sermo/internal/checks"
)

func TestSnapshotsRoundTrip(t *testing.T) {
	s := NewSnapshots()
	if s.Get("web") != nil {
		t.Fatal("unobserved service should have no snapshot")
	}
	s.Publish("web", map[string]checks.Result{
		"http": {Check: "http", OK: true, Message: "status 200"},
		"warn": {Check: "warn", OK: false, Optional: true},
	})
	got := s.Get("web")
	if len(got) != 2 || !got["http"].OK || got["http"].Message != "status 200" {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if got["warn"].OK || !got["warn"].Optional {
		t.Fatalf("optional/failed not preserved: %+v", got["warn"])
	}
}

func TestWorkerPublishesCache(t *testing.T) {
	var published map[string]checks.Result
	w := &Worker{
		Service: "web",
		Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			return map[string]checks.Result{"http": {Check: "http", OK: true}}
		},
		Publish: func(c map[string]checks.Result) { published = c },
	}
	w.RunCycle(context.Background())
	if published == nil || !published["http"].OK {
		t.Fatalf("worker did not publish its cache: %+v", published)
	}
}

func TestWorkerPausedDoesNotPublish(t *testing.T) {
	called := false
	w := &Worker{
		Service:  "web",
		IsPaused: func() bool { return true },
		Checks:   func(context.Context, checks.Deps) map[string]checks.Result { return nil },
		Publish:  func(map[string]checks.Result) { called = true },
	}
	w.RunCycle(context.Background())
	if called {
		t.Fatal("a paused cycle must not publish a snapshot")
	}
}
