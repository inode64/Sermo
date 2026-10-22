package state

import (
	"context"
	"strings"
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

	events, err := s.RecentEventsBefore(0, 0)
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
	limited, err := s.RecentEventsBefore(0, 1)
	if err != nil {
		t.Fatalf("RecentEvents(limit): %v", err)
	}
	if len(limited) != 1 || limited[0].Watch != "storage-root" {
		t.Fatalf("limited events = %+v, want newest watch event", limited)
	}

	n, err := s.PruneEvents(context.Background(), recent)
	if err != nil {
		t.Fatalf("PruneEvents(before recent): %v", err)
	}
	if n != 1 {
		t.Fatalf("PruneEvents removed %d, want 1", n)
	}
	events, err = s.RecentEventsBefore(0, 0)
	if err != nil {
		t.Fatalf("RecentEvents(after prune): %v", err)
	}
	if len(events) != 1 || events[0].Message != "recent" {
		t.Fatalf("after prune events = %+v, want only recent", events)
	}

	n, err = s.PruneEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("PruneEvents(all): %v", err)
	}
	if n != 1 {
		t.Fatalf("PruneEvents(all) removed %d, want 1", n)
	}
	events, err = s.RecentEventsBefore(0, 0)
	if err != nil {
		t.Fatalf("RecentEvents(after clear): %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events after clear = %+v, want none", events)
	}
}

func TestStoreActiveEventNotifyIncidentsAndDelete(t *testing.T) {
	s := openTemp(t)
	at := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	for _, rec := range []EventNotifyRecord{
		{IncidentKey: "open", Notifier: "ops", Phase: "firing", Active: true, LastSentAt: at},
		{IncidentKey: "open", Notifier: "mail", Phase: "firing", Active: true, LastSentAt: at},
		{IncidentKey: "closed", Notifier: "ops", Phase: "recovered", LastSentAt: at},
	} {
		if err := s.SetEventNotifyState(rec); err != nil {
			t.Fatalf("SetEventNotifyState: %v", err)
		}
	}
	keys, err := s.ActiveEventNotifyIncidents()
	if err != nil || len(keys) != 1 || keys[0] != "open" {
		t.Fatalf("ActiveEventNotifyIncidents() = %v, %v; want [open]", keys, err)
	}
	if err := s.DeleteEventNotifyIncident("open"); err != nil {
		t.Fatalf("DeleteEventNotifyIncident: %v", err)
	}
	for _, notifier := range []string{"ops", "mail"} {
		if _, found, err := s.EventNotifyState("open", notifier); err != nil || found {
			t.Fatalf("open incident at %s after delete: found=%v err=%v", notifier, found, err)
		}
	}
	if rec, found, err := s.EventNotifyState("closed", "ops"); err != nil || !found || rec.Phase != "recovered" {
		t.Fatalf("unrelated incident changed: %+v found=%v err=%v", rec, found, err)
	}
}

// A target's newest events come straight off the service index, without
// sorting every event the service ever recorded.
func TestRecentEventsForServiceUsesTheServiceIDIndex(t *testing.T) {
	s := openTemp(t)
	plan := queryPlan(t, s, eventSelectPrefix+` WHERE service = ? ORDER BY id DESC LIMIT ?;`, "web", 1)
	if !strings.Contains(plan, "event_log_service_id_idx") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("query plan = %q, want event_log_service_id_idx without a sort", plan)
	}
}

// The newest operation outcome per service survives later events of other
// kinds and ignores rows that record no outcome.
func TestLatestOperationOutcomes(t *testing.T) {
	s := openTemp(t)
	at := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	rows := []EventRecord{
		{Service: "alloy", Kind: "action", Action: "restart", Status: "ok"},
		{Service: "alloy", Kind: "suppressed", Action: "restart", Status: "blocked", Message: "identity"},
		{Service: "alloy", Kind: "firing", Message: "check service"},
		{Service: "alloy", Kind: "dry-run", Rule: "ready", Action: "restart", Message: "would restart"},
		{Service: "alloy", Kind: "suppressed", Rule: "ready", Action: "restart", Message: "cooldown"},
		{Service: "alloy", Kind: "warning", Action: "release_lock", Status: "ok"},
		{Service: "web", Kind: "error", Action: "stop", Status: "orphan_processes"},
		{Service: "web", Kind: "action", Action: "start", Status: "ok"},
		{Watch: "storage-root", Kind: "error", Action: "restart", Status: "failed"},
		{Service: "db", Kind: "firing", Message: "never operated"},
	}
	for i, row := range rows {
		row.At = at.Add(time.Duration(i) * time.Second)
		if _, err := s.RecordEvent(row); err != nil {
			t.Fatal(err)
		}
	}
	actions := []string{"restart", "start", "stop"}
	got, err := s.LatestOperationOutcomes("", actions)
	if err != nil {
		t.Fatal(err)
	}
	byService := map[string]EventRecord{}
	for _, rec := range got {
		byService[rec.Service] = rec
	}
	if len(byService) != 2 || byService["alloy"].Status != "blocked" || byService["alloy"].Message != "identity" ||
		byService["web"].Action != "start" || byService["web"].Status != "ok" {
		t.Fatalf("latest outcomes = %+v", got)
	}
	one, err := s.LatestOperationOutcomes("web", actions)
	if err != nil || len(one) != 1 || one[0].Service != "web" || one[0].Action != "start" {
		t.Fatalf("one service = %+v, %v", one, err)
	}
	if none, err := s.LatestOperationOutcomes("", nil); err != nil || none != nil {
		t.Fatalf("no actions = %+v, %v", none, err)
	}
}

func TestLatestOperationOutcomesUsesTheOutcomeIndex(t *testing.T) {
	s := openTemp(t)
	for _, tc := range []struct {
		oneService bool
		args       []any
	}{
		{args: []any{"restart", "stop"}},
		{oneService: true, args: []any{"web", "restart", "stop"}},
	} {
		plan := queryPlan(t, s, latestOperationOutcomesQuery(tc.oneService, 2), tc.args...)
		if !strings.Contains(plan, "event_log_service_outcome_idx") || containsAny(plan, "SCAN event_log;", "TEMP B-TREE") {
			t.Fatalf("one service=%v: query plan = %q, want the partial outcome index", tc.oneService, plan)
		}
	}
}
