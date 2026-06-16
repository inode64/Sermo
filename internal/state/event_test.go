package state

import (
	"testing"
	"time"
)

func TestStoreEventsRoundTripAndPrune(t *testing.T) {
	s := openTemp(t)
	old := time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)
	recent := old.Add(time.Hour)

	if err := s.RecordEvent(EventRecord{At: old, Service: "web", Kind: "action", Action: "restart", Status: "ok", Message: "old"}); err != nil {
		t.Fatalf("RecordEvent(old): %v", err)
	}
	if err := s.RecordEvent(EventRecord{At: recent, Watch: "disk-root", Kind: "hook-failed", Message: "recent"}); err != nil {
		t.Fatalf("RecordEvent(recent): %v", err)
	}

	events, err := s.RecentEvents(0)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 2 || events[0].Message != "recent" || events[1].Service != "web" {
		t.Fatalf("events = %+v, want newest first with service fields", events)
	}
	limited, err := s.RecentEvents(1)
	if err != nil {
		t.Fatalf("RecentEvents(limit): %v", err)
	}
	if len(limited) != 1 || limited[0].Watch != "disk-root" {
		t.Fatalf("limited events = %+v, want newest watch event", limited)
	}

	n, err := s.PruneEvents(recent)
	if err != nil {
		t.Fatalf("PruneEvents(before recent): %v", err)
	}
	if n != 1 {
		t.Fatalf("PruneEvents removed %d, want 1", n)
	}
	events, err = s.RecentEvents(0)
	if err != nil {
		t.Fatalf("RecentEvents(after prune): %v", err)
	}
	if len(events) != 1 || events[0].Message != "recent" {
		t.Fatalf("after prune events = %+v, want only recent", events)
	}

	n, err = s.PruneEvents(time.Time{})
	if err != nil {
		t.Fatalf("PruneEvents(all): %v", err)
	}
	if n != 1 {
		t.Fatalf("PruneEvents(all) removed %d, want 1", n)
	}
	events, err = s.RecentEvents(0)
	if err != nil {
		t.Fatalf("RecentEvents(after clear): %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events after clear = %+v, want none", events)
	}
}
