package app

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/state"
)

type checkSLACapture struct {
	records []checkSLARecord
}

type checkSLARecord struct {
	check string
	up    bool
}

func (c *checkSLACapture) RecordSLA(string, bool, time.Time) error { return nil }
func (c *checkSLACapture) RecordCheckSLA(_, check string, up bool, _ time.Time) error {
	c.records = append(c.records, checkSLARecord{check: check, up: up})
	return nil
}

func TestRecordHealthReflectsRequiredChecks(t *testing.T) {
	cases := []struct {
		name  string
		cache map[string]checks.Result
		want  bool
	}{
		{"all required ok", map[string]checks.Result{"http": {OK: true}}, true},
		{"required failed", map[string]checks.Result{"http": {OK: false}}, false},
		{"healthy condition check is up", map[string]checks.Result{"cert": {OK: false, Condition: true}}, true},
		{"firing condition check is down", map[string]checks.Result{"cert": {OK: true, Condition: true}}, false},
		{"optional failed still up", map[string]checks.Result{"http": {OK: true}, "warn": {OK: false, Optional: true}}, true},
		{"no checks vacuously up", map[string]checks.Result{}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got *bool
			w := &Worker{
				Service: "web",
				Checks:  func(context.Context, checks.Deps) map[string]checks.Result { return tc.cache },
				RecordCycle: func(_ context.Context, cycle cycleRecord) {
					got = &cycle.up
				},
			}
			w.RunCycle(context.Background())
			if got == nil {
				t.Fatal("RecordCycle was not called for an observed cycle")
			}
			if *got != tc.want {
				t.Fatalf("recorded up=%v, want %v", *got, tc.want)
			}
		})
	}
}

func TestRecordHealthSkippedWhenPaused(t *testing.T) {
	called := false
	w := &Worker{
		Service:  "web",
		IsPaused: func() bool { return true },
		Checks: func(context.Context, checks.Deps) map[string]checks.Result {
			return map[string]checks.Result{"http": {OK: false}}
		},
		RecordCycle: func(context.Context, cycleRecord) { called = true },
	}
	w.RunCycle(context.Background())
	if called {
		t.Fatal("paused cycle must not record an SLA sample")
	}
}

func TestCheckSLARecorderOnlyRecordsRanNonSkippedChecks(t *testing.T) {
	store := &checkSLACapture{}
	writer := newCycleWriter(Deps{
		SLA: store,
		Now: func() time.Time { return time.Unix(0, 0) },
	}, "svc", nil)
	if writer == nil {
		t.Fatal("expected cycle writer")
	}
	writer.RecordCycle(context.Background(), cycleRecord{
		cache: map[string]checks.Result{
			"http":   {Check: "http", OK: false},
			"cert":   {Check: "cert", OK: false, Condition: true},
			"cached": {Check: "cached", OK: true},
			"gated":  {Check: "gated", OK: true, Skipped: true},
		},
		ran:                map[string]bool{"http": true, "cert": true, "gated": true},
		up:                 true,
		recordAvailability: true,
	})

	got := map[string]bool{}
	for _, r := range store.records {
		got[r.check] = r.up
	}
	if len(got) != 2 || got["http"] || !got["cert"] {
		t.Fatalf("records = %+v, want http=false and cert=true", store.records)
	}
}

// An advisory records no SLA series either: a warning is something to look at,
// not downtime to hold against the service. A warning that is *not* failing has
// nothing to grade, so its healthy sample still counts.
func TestCheckSLARecorderSkipsFailingAdvisories(t *testing.T) {
	store := &checkSLACapture{}
	writer := newCycleWriter(Deps{
		SLA: store,
		Now: func() time.Time { return time.Unix(0, 0) },
	}, "svc", nil)
	writer.RecordCycle(context.Background(), cycleRecord{
		cache: map[string]checks.Result{
			"disk-speed": {Check: "disk-speed", OK: false, Severity: checks.SeverityWarning},
			"disk-ok":    {Check: "disk-ok", OK: true, Severity: checks.SeverityWarning},
			"http":       {Check: "http", OK: false},
		},
		ran:                map[string]bool{"disk-speed": true, "disk-ok": true, "http": true},
		up:                 true,
		recordAvailability: true,
	})

	got := map[string]bool{}
	for _, r := range store.records {
		got[r.check] = r.up
	}
	if _, recorded := got["disk-speed"]; recorded {
		t.Errorf("records = %+v, want no series for a failing advisory", store.records)
	}
	if up, recorded := got["disk-ok"]; !recorded || !up {
		t.Errorf("records = %+v, want a healthy advisory still recorded up", store.records)
	}
	if up, recorded := got["http"]; !recorded || up {
		t.Errorf("records = %+v, want http recorded down", store.records)
	}
}

// A failing advisory must not take the service down with it, exactly as a
// declared-optional check never has.
func TestRequiredChecksOKIgnoresAdvisories(t *testing.T) {
	if !requiredChecksOK(map[string]checks.Result{
		"http":       {OK: true},
		"disk-speed": {OK: false, Severity: checks.SeverityWarning},
	}) {
		t.Error("a failing advisory took the service down, want availability unaffected")
	}
	if requiredChecksOK(map[string]checks.Result{"http": {OK: false, Severity: checks.SeverityError}}) {
		t.Error("a failing error-severity check left the service up")
	}
}

// A state sensor gets no SLA series at all. Recording it would be a permanent
// 0% (a backup is idle almost always) or, once availability stops being the
// raw flag, a meaningless 100% — neither is uptime.
func TestCheckSLARecorderSkipsStateSensors(t *testing.T) {
	store := &checkSLACapture{}
	writer := newCycleWriter(Deps{
		SLA: store,
		Now: func() time.Time { return time.Unix(0, 0) },
	}, "svc", nil)
	writer.RecordCycle(context.Background(), cycleRecord{
		cache: map[string]checks.Result{
			"backup": {Check: "backup", OK: false, Reports: checks.ReportsState},
			"busy":   {Check: "busy", OK: true, Reports: checks.ReportsState},
			"http":   {Check: "http", OK: true},
		},
		ran:                map[string]bool{"backup": true, "busy": true, "http": true},
		up:                 true,
		recordAvailability: true,
	})

	got := map[string]bool{}
	for _, r := range store.records {
		got[r.check] = r.up
	}
	if len(got) != 1 || !got["http"] {
		t.Fatalf("records = %+v, want only http recorded", store.records)
	}
}

// failingBatchStore is an SLA store whose transaction always fails with a fixed
// error, so RecordCycle's error reporting can be exercised directly.
type failingBatchStore struct {
	checkSLACapture
	err error
}

func (f *failingBatchStore) WithBatch(context.Context, func(state.Batch) error) error { return f.err }

// A stop or a reload cancels the cycle context mid-batch. That is the daemon
// shutting down, not a storage fault, and writing an error event for it left
// every in-flight service showing "record cycle: begin state batch: context
// canceled" as its newest event for days after the restart.
func TestRecordCycleDoesNotReportCancellationAsError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantEvent bool
	}{
		{name: "cancellation is silent", err: context.Canceled, wantEvent: false},
		{name: "wrapped cancellation is silent", err: fmt.Errorf("begin state batch: %w", context.Canceled), wantEvent: false},
		{name: "real storage failure still reports", err: errors.New("disk I/O error"), wantEvent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &failingBatchStore{err: tt.err}
			var events []Event
			writer := newCycleWriter(Deps{
				SLA:  store,
				Now:  func() time.Time { return time.Unix(0, 0) },
				Emit: func(e Event) { events = append(events, e) },
			}, "svc", nil)
			if writer == nil {
				t.Fatal("expected cycle writer")
			}
			writer.RecordCycle(context.Background(), cycleRecord{up: true, recordAvailability: true})

			if tt.wantEvent {
				if len(events) != 1 || events[0].Kind != eventKindError {
					t.Fatalf("events = %+v, want one error event", events)
				}
				return
			}
			if len(events) != 0 {
				t.Fatalf("events = %+v, want none for %v", events, tt.err)
			}
		})
	}
}
