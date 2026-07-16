package app

import (
	"testing"

	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/state"
)

func TestSyncManualActionMonitoringPausesAndRestores(t *testing.T) {
	store := newFakeStore()
	result := operation.Result{Service: "web", Action: string(rules.ActionStop), Status: operation.ResultOK}

	change, err := SyncManualActionMonitoringWithActive(store, "web", string(rules.ActionStop), result, state.SourceCLIManualStop, state.SourceCLI, false)
	if err != nil {
		t.Fatalf("stop sync: %v", err)
	}
	if !change.Changed || change.Action != eventActionUnmonitor || change.Monitored {
		t.Fatalf("stop change = %+v", change)
	}
	if store.active["web"] || store.source["web"] != state.SourceCLIManualStop {
		t.Fatalf("store after stop active=%v source=%q", store.active["web"], store.source["web"])
	}

	result = operation.Result{Service: "web", Action: string(rules.ActionStart), Status: operation.ResultOK}
	change, err = SyncManualActionMonitoringWithActive(store, "web", string(rules.ActionStart), result, state.SourceWebManualStop, state.SourceWeb, false)
	if err != nil {
		t.Fatalf("start sync: %v", err)
	}
	if !change.Changed || change.Action != eventActionMonitor || !change.Monitored {
		t.Fatalf("start change = %+v", change)
	}
	if !store.active["web"] || store.source["web"] != state.SourceWeb {
		t.Fatalf("store after start active=%v source=%q", store.active["web"], store.source["web"])
	}
}

func TestSyncManualActionMonitoringPreservesExistingUnmonitor(t *testing.T) {
	store := newFakeStore()
	store.active["web"] = false
	store.source["web"] = state.SourceCLI

	result := operation.Result{Service: "web", Action: string(rules.ActionStop), Status: operation.ResultOK}
	change, err := SyncManualActionMonitoringWithActive(store, "web", string(rules.ActionStop), result, state.SourceWebManualStop, state.SourceWeb, false)
	if err != nil {
		t.Fatalf("stop sync: %v", err)
	}
	if change.Changed {
		t.Fatalf("stop should preserve existing unmonitor, got %+v", change)
	}
	if store.source["web"] != state.SourceCLI {
		t.Fatalf("source changed to %q", store.source["web"])
	}

	result = operation.Result{Service: "web", Action: string(rules.ActionStart), Status: operation.ResultOK}
	change, err = SyncManualActionMonitoringWithActive(store, "web", string(rules.ActionStart), result, state.SourceWebManualStop, state.SourceWeb, false)
	if err != nil {
		t.Fatalf("start sync: %v", err)
	}
	if change.Changed || store.active["web"] || store.source["web"] != state.SourceCLI {
		t.Fatalf("start should not restore existing unmonitor, change=%+v active=%v source=%q", change, store.active["web"], store.source["web"])
	}
}

func TestSyncManualActionMonitoringIgnoresFailedOperation(t *testing.T) {
	store := newFakeStore()
	result := operation.Result{Service: "web", Action: string(rules.ActionStop), Status: operation.ResultFailed}

	change, err := SyncManualActionMonitoringWithActive(store, "web", string(rules.ActionStop), result, state.SourceCLIManualStop, state.SourceCLI, false)
	if err != nil {
		t.Fatalf("failed op sync: %v", err)
	}
	if change.Changed {
		t.Fatalf("failed op changed monitoring: %+v", change)
	}
	if _, found := store.active["web"]; found {
		t.Fatal("failed op should not write monitoring state")
	}
}

func TestSyncManualActionMonitoringRestoresPostflightFailedActiveStart(t *testing.T) {
	store := newFakeStore()
	store.active["web"] = false
	store.source["web"] = state.SourceCLIManualStop

	result := operation.Result{Service: "web", Action: string(rules.ActionStart), Status: operation.ResultPostflightFailed}
	change, err := SyncManualActionMonitoringWithActive(store, "web", string(rules.ActionStart), result, state.SourceCLIManualStop, state.SourceCLI, true)
	if err != nil {
		t.Fatalf("active postflight sync: %v", err)
	}
	if !change.Changed || !change.Monitored || change.Action != eventActionMonitor {
		t.Fatalf("active postflight change = %+v", change)
	}
	if !store.active["web"] || store.source["web"] != state.SourceCLI {
		t.Fatalf("store after active postflight start active=%v source=%q", store.active["web"], store.source["web"])
	}

	store.active["web"] = false
	store.source["web"] = state.SourceCLIManualStop
	change, err = SyncManualActionMonitoringWithActive(store, "web", string(rules.ActionStart), result, state.SourceCLIManualStop, state.SourceCLI, false)
	if err != nil {
		t.Fatalf("inactive postflight sync: %v", err)
	}
	if change.Changed || store.active["web"] || store.source["web"] != state.SourceCLIManualStop {
		t.Fatalf("inactive postflight should not restore, change=%+v active=%v source=%q", change, store.active["web"], store.source["web"])
	}
}
