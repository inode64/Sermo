package app

import (
	"context"
	"testing"

	"sermo/internal/checks"
)

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
