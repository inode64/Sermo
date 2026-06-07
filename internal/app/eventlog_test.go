package app

import "testing"

func TestEventLogRecentNewestFirst(t *testing.T) {
	l := NewEventLog(10)
	l.Add(Event{Service: "a", Kind: "action", Message: "1"})
	l.Add(Event{Service: "b", Kind: "alert", Message: "2"})
	l.Add(Event{Service: "a", Kind: "error", Message: "3"})

	all := l.Recent("", 0)
	if len(all) != 3 || all[0].Message != "3" || all[2].Message != "1" {
		t.Fatalf("recent newest-first wrong: %+v", all)
	}
	if got := l.Recent("", 2); len(got) != 2 || got[0].Message != "3" {
		t.Fatalf("limit not applied: %+v", got)
	}
}

func TestEventLogPerService(t *testing.T) {
	l := NewEventLog(10)
	l.Add(Event{Service: "a", Message: "a1"})
	l.Add(Event{Watch: "disk", Message: "w1"}) // host watch, no service
	l.Add(Event{Service: "b", Message: "b1"})
	l.Add(Event{Service: "a", Message: "a2"})

	a := l.Recent("a", 0)
	if len(a) != 2 || a[0].Message != "a2" || a[1].Message != "a1" {
		t.Fatalf("per-service filter wrong: %+v", a)
	}
	// the global feed includes the watch event
	if len(l.Recent("", 0)) != 4 {
		t.Fatal("global feed should include the watch event")
	}
}

func TestEventLogRingEviction(t *testing.T) {
	l := NewEventLog(3)
	for _, m := range []string{"1", "2", "3", "4", "5"} {
		l.Add(Event{Service: "s", Message: m})
	}
	got := l.Recent("", 0)
	if len(got) != 3 || got[0].Message != "5" || got[2].Message != "3" {
		t.Fatalf("ring eviction wrong: %+v", got)
	}
}

func TestMultiEmit(t *testing.T) {
	var a, b []Event
	emit := MultiEmit(
		func(e Event) { a = append(a, e) },
		nil, // skipped
		func(e Event) { b = append(b, e) },
	)
	emit(Event{Kind: "action"})
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("MultiEmit did not fan out: a=%d b=%d", len(a), len(b))
	}
}
