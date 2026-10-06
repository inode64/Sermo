package app

import (
	"testing"
	"time"

	"sermo/internal/checks"
)

func TestCheckViewCompactSummaryPreservesDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name           string
		observation    checks.ObservationState
		stale, skipped bool
		want           string
	}{
		{name: "healthy", observation: checks.ObservationHealthy, want: "Probe succeeded"},
		{name: "failing", observation: checks.ObservationFailing},
		{name: "unavailable", observation: checks.ObservationUnavailable},
		{name: "neutral", observation: checks.ObservationNeutral},
		{name: "stale", observation: checks.ObservationHealthy, stale: true},
		{name: "skipped", observation: checks.ObservationSkipped, skipped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := CheckSnapshot{OK: true, Ran: true, At: time.Unix(1000, 0), Message: "original diagnostic", Observation: tc.observation, Skipped: tc.skipped,
				Data: map[string]any{checks.DataKeySummary: "Probe succeeded", checks.DataKeyProtocol: "redis", checks.DataKeyHost: "localhost", checks.DataKeyPort: 6379}}
			o := serviceObservation{rawSnapshots: map[string]CheckSnapshot{"health": snap}}
			if !tc.stale {
				o.snapshots = o.rawSnapshots
			}
			got := o.checkView("health", &webEntry{checkTypes: map[string]string{"health": "redis"}})
			if got.Summary != tc.want {
				t.Fatalf("summary = %q, want %q", got.Summary, tc.want)
			}
			if !tc.stale && got.Message != snap.Message {
				t.Fatalf("diagnostic = %q", got.Message)
			}
			if tc.stale && len(got.Readings) != 0 {
				t.Fatal("stale readings exposed as current")
			}
		})
	}
}
