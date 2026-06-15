package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
)

type checkSLACapture struct {
	records []checkSLARecord
}

type checkSLARecord struct {
	check string
	up    bool
}

func (c *checkSLACapture) RecordSLA(string, bool, time.Time) error { return nil }
func (c *checkSLACapture) RecordCheckSLA(_ string, check string, up bool, _ time.Time) error {
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
		{"optional failed still up", map[string]checks.Result{"http": {OK: true}, "warn": {OK: false, Optional: true}}, true},
		{"no checks vacuously up", map[string]checks.Result{}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got *bool
			w := &Worker{
				Service:      "web",
				Checks:       func(context.Context, checks.Deps) map[string]checks.Result { return tc.cache },
				RecordHealth: func(up bool) { got = &up },
			}
			w.RunCycle(context.Background())
			if got == nil {
				t.Fatal("RecordHealth was not called for an observed cycle")
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
		RecordHealth: func(bool) { called = true },
	}
	w.RunCycle(context.Background())
	if called {
		t.Fatal("paused cycle must not record an SLA sample")
	}
}

func TestCheckSLARecorderOnlyRecordsRanNonSkippedChecks(t *testing.T) {
	store := &checkSLACapture{}
	record := checkSLARecorder(Deps{
		SLA: store,
		Now: func() time.Time { return time.Unix(0, 0) },
	}, "svc")
	if record == nil {
		t.Fatal("expected check SLA recorder")
	}
	record(map[string]checks.Result{
		"http":   {Check: "http", OK: false},
		"cached": {Check: "cached", OK: true},
		"gated":  {Check: "gated", OK: true, Skipped: true},
	}, map[string]bool{"http": true, "gated": true})

	if len(store.records) != 1 || store.records[0] != (checkSLARecord{check: "http", up: false}) {
		t.Fatalf("records = %+v, want only http=false", store.records)
	}
}
