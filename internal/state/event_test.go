package state

import (
	"testing"
	"time"
)

func TestStoreEventsRoundTripAndPrune(t *testing.T) {
	s := openTemp(t)
	old := time.Date(2026, 6, 13, 10, 0, 0, 0, time.UTC)
	recent := old.Add(time.Hour)

	oldID, err := s.RecordEvent(EventRecord{At: old, Service: "web", Kind: "action", Action: "restart", Status: "ok", Message: "old"})
	if err != nil {
		t.Fatalf("RecordEvent(old): %v", err)
	}
	recentID, err := s.RecordEvent(EventRecord{At: recent, Watch: "storage-root", Kind: "hook-failed", Message: "recent"})
	if err != nil {
		t.Fatalf("RecordEvent(recent): %v", err)
	}
	if oldID <= 0 || recentID <= oldID {
		t.Fatalf("event IDs old=%d recent=%d, want increasing positive IDs", oldID, recentID)
	}

	events, err := s.RecentEvents(0)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 2 || events[0].Message != "recent" || events[1].Service != "web" {
		t.Fatalf("events = %+v, want newest first with service fields", events)
	}
	if events[0].ID != recentID || events[1].ID != oldID {
		t.Fatalf("event IDs = [%d %d], want [%d %d]", events[0].ID, events[1].ID, recentID, oldID)
	}
	older, err := s.RecentEventsBefore(recentID, 10)
	if err != nil {
		t.Fatalf("RecentEventsBefore: %v", err)
	}
	if len(older) != 1 || older[0].ID != oldID {
		t.Fatalf("older events = %+v, want only ID %d", older, oldID)
	}
	limited, err := s.RecentEvents(1)
	if err != nil {
		t.Fatalf("RecentEvents(limit): %v", err)
	}
	if len(limited) != 1 || limited[0].Watch != "storage-root" {
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
