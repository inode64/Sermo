package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/servicemgr"
)

func TestObservationContractReachesPersistenceEventsAndWeb(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		result      checks.Result
		observation checks.ObservationState
		wantEvents  int
		wantFailing int
		wantHealth  string
	}{
		{name: "healthy", result: checks.Result{OK: true}, observation: checks.ObservationHealthy, wantHealth: TargetStateOK},
		{name: "failing", result: checks.Result{OK: false}, observation: checks.ObservationFailing, wantEvents: 1, wantFailing: 1, wantHealth: checkHealthFailing},
		{name: "unavailable", result: checks.Result{OK: false, Unavailable: true}, observation: checks.ObservationUnavailable, wantEvents: 1, wantFailing: 1, wantHealth: checkHealthFailing},
		{name: "skipped", result: checks.Result{OK: false, Skipped: true}, observation: checks.ObservationSkipped, wantHealth: TargetStateOK},
		{name: "neutral", result: checks.Result{OK: false, Reports: checks.ReportsState}, observation: checks.ObservationNeutral, wantHealth: TargetStateOK},
		{name: "condition healthy", result: checks.Result{OK: false, Condition: true}, observation: checks.ObservationHealthy, wantHealth: TargetStateOK},
		{name: "condition firing", result: checks.Result{OK: true, Condition: true}, observation: checks.ObservationFailing, wantEvents: 1, wantFailing: 1, wantHealth: checkHealthFailing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &snapshotStoreFake{}
			snapshots, err := NewPersistentSnapshots(store, nil)
			if err != nil {
				t.Fatalf("NewPersistentSnapshots: %v", err)
			}
			var events []Event
			result := tt.result
			result.Check = "probe"
			worker := &Worker{
				Service: "demo",
				Checks: func(context.Context, checks.Deps) map[string]checks.Result {
					return map[string]checks.Result{"probe": result}
				},
				Publish: func(cache map[string]checks.Result, _ map[string]bool) {
					snapshots.PublishWithCheckTypes("demo", cache, map[string]bool{"probe": true}, map[string]string{"probe": "service"})
				},
				Emit: func(event Event) { events = append(events, event) },
			}
			worker.RunCycle(context.Background())

			persisted := store.service["demo"]["probe"]
			if persisted.Observation != string(tt.observation) {
				t.Fatalf("persisted observation = %q, want %q", persisted.Observation, tt.observation)
			}
			if len(events) != tt.wantEvents {
				t.Fatalf("events = %+v, want %d", events, tt.wantEvents)
			}

			entry := &webEntry{
				checkNames: []string{"probe"},
				checkTypes: map[string]string{"probe": "service"},
				interval:   time.Minute,
				status: func(context.Context) (servicemgr.Status, error) {
					return servicemgr.StatusActive, nil
				},
			}
			backend := &WebBackend{
				order:     []string{"demo"},
				entries:   map[string]*webEntry{"demo": entry},
				snapshots: snapshots,
			}
			service := backend.view(context.Background(), "demo", entry)
			if service.ChecksFailing != tt.wantFailing || service.CheckHealth != tt.wantHealth {
				t.Fatalf("web check health = %q failing=%d, want %q failing=%d", service.CheckHealth, service.ChecksFailing, tt.wantHealth, tt.wantFailing)
			}
		})
	}
}
