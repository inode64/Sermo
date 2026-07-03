package app

import (
	"testing"

	"sermo/internal/operation"
	"sermo/internal/state"
)

func TestSyncManualActionMonitoringPausesAndRestores(t *testing.T) {
	store := newFakeStore()
	result := operation.Result{Service: "web", Action: "stop", Status: operation.ResultOK}

	change, err := SyncManualActionMonitoring(store, "web", "stop", result, state.SourceCLIManualStop, state.SourceCLI)
	if err != nil {
		t.Fatalf("stop sync: %v", err)
	}
	if !change.Changed || change.Action != "unmonitor" || change.Monitored {
		t.Fatalf("stop change = %+v", change)
	}
	if store.active["web"] || store.source["web"] != state.SourceCLIManualStop {
		t.Fatalf("store after stop active=%v source=%q", store.active["web"], store.source["web"])
	}

	result = operation.Result{Service: "web", Action: "start", Status: operation.ResultOK}
	change, err = SyncManualActionMonitoring(store, "web", "start", result, state.SourceWebManualStop, state.SourceWeb)
	if err != nil {
		t.Fatalf("start sync: %v", err)
	}
	if !change.Changed || change.Action != "monitor" || !change.Monitored {
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

	result := operation.Result{Service: "web", Action: "stop", Status: operation.ResultOK}
	change, err := SyncManualActionMonitoring(store, "web", "stop", result, state.SourceWebManualStop, state.SourceWeb)
	if err != nil {
		t.Fatalf("stop sync: %v", err)
	}
	if change.Changed {
		t.Fatalf("stop should preserve existing unmonitor, got %+v", change)
	}
	if store.source["web"] != state.SourceCLI {
		t.Fatalf("source changed to %q", store.source["web"])
	}

	result = operation.Result{Service: "web", Action: "start", Status: operation.ResultOK}
	change, err = SyncManualActionMonitoring(store, "web", "start", result, state.SourceWebManualStop, state.SourceWeb)
	if err != nil {
		t.Fatalf("start sync: %v", err)
	}
	if change.Changed || store.active["web"] || store.source["web"] != state.SourceCLI {
		t.Fatalf("start should not restore existing unmonitor, change=%+v active=%v source=%q", change, store.active["web"], store.source["web"])
	}
}

func TestSyncManualActionMonitoringIgnoresFailedOperation(t *testing.T) {
	store := newFakeStore()
	result := operation.Result{Service: "web", Action: "stop", Status: operation.ResultFailed}

	change, err := SyncManualActionMonitoring(store, "web", "stop", result, state.SourceCLIManualStop, state.SourceCLI)
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
