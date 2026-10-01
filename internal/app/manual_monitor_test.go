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

	change, err := SyncManualActionMonitoring(store, "web", string(rules.ActionStop), result, state.SourceCLIManualStop, state.SourceCLI, false)
	if err != nil {
		t.Fatalf("stop sync: %v", err)
	}
	if !change.Changed || change.Action != eventActionUnmonitor {
		t.Fatalf("stop change = %+v", change)
	}
	if store.active["web"] || store.source["web"] != state.SourceCLIManualStop {
		t.Fatalf("store after stop active=%v source=%q", store.active["web"], store.source["web"])
	}

	result = operation.Result{Service: "web", Action: string(rules.ActionStart), Status: operation.ResultOK}
	change, err = SyncManualActionMonitoring(store, "web", string(rules.ActionStart), result, state.SourceWebManualStop, state.SourceWeb, false)
	if err != nil {
		t.Fatalf("start sync: %v", err)
	}
	if !change.Changed || change.Action != eventActionMonitor {
		t.Fatalf("start change = %+v", change)
	}
	if !store.active["web"] || store.source["web"] != state.SourceWeb {
		t.Fatalf("store after start active=%v source=%q", store.active["web"], store.source["web"])
	}
}

// A manual pause takes the workload out of service like a stop: monitoring is
// paused under the manual-stop source, so resume restores it.
func TestSyncManualActionMonitoringPauseAndResume(t *testing.T) {
	store := newFakeStore()
	result := operation.Result{Service: "vm", Action: operation.ActionPause, Status: operation.ResultOK}
	change, err := SyncManualActionMonitoring(store, "vm", operation.ActionPause, result, state.SourceWebManualStop, state.SourceWeb, false)
	if err != nil {
		t.Fatalf("pause sync: %v", err)
	}
	if !change.Changed || change.Action != eventActionUnmonitor || change.Message != "monitoring paused after manual pause" {
		t.Fatalf("pause change = %+v", change)
	}
	if store.active["vm"] || store.source["vm"] != state.SourceWebManualStop {
		t.Fatalf("store after pause active=%v source=%q", store.active["vm"], store.source["vm"])
	}

	result = operation.Result{Service: "vm", Action: string(rules.ActionResume), Status: operation.ResultOK}
	change, err = SyncManualActionMonitoring(store, "vm", string(rules.ActionResume), result, state.SourceWebManualStop, state.SourceWeb, false)
	if err != nil {
		t.Fatalf("resume sync: %v", err)
	}
	if !change.Changed || change.Action != eventActionMonitor || change.Message != "monitoring resumed after manual resume" || !store.active["vm"] {
		t.Fatalf("resume change = %+v active=%v", change, store.active["vm"])
	}

	failed := operation.Result{Service: "vm", Action: operation.ActionPause, Status: operation.ResultFailed}
	if change, err := SyncManualActionMonitoring(store, "vm", operation.ActionPause, failed, state.SourceWebManualStop, state.SourceWeb, false); err != nil || change.Changed {
		t.Fatalf("failed pause change = %+v, %v; want no monitoring change", change, err)
	}
}

func TestSyncManualActionMonitoringRestoresAfterRepair(t *testing.T) {
	store := newFakeStore()
	store.active["web"] = false
	store.source["web"] = state.SourceCLIManualStop
	result := operation.Result{Service: "web", Action: operation.ActionRepair, Status: operation.ResultOK}

	change, err := SyncManualActionMonitoring(store, "web", operation.ActionRepair, result, state.SourceCLIManualStop, state.SourceCLI, false)

	if err != nil {
		t.Fatalf("repair sync: %v", err)
	}
	if !change.Changed || change.Action != eventActionMonitor {
		t.Fatalf("repair change = %+v", change)
	}
}

func TestSyncManualActionMonitoringPreservesExistingUnmonitor(t *testing.T) {
	store := newFakeStore()
	store.active["web"] = false
	store.source["web"] = state.SourceCLI

	result := operation.Result{Service: "web", Action: string(rules.ActionStop), Status: operation.ResultOK}
	change, err := SyncManualActionMonitoring(store, "web", string(rules.ActionStop), result, state.SourceWebManualStop, state.SourceWeb, false)
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
	change, err = SyncManualActionMonitoring(store, "web", string(rules.ActionStart), result, state.SourceWebManualStop, state.SourceWeb, false)
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

	change, err := SyncManualActionMonitoring(store, "web", string(rules.ActionStop), result, state.SourceCLIManualStop, state.SourceCLI, false)
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
	change, err := SyncManualActionMonitoring(store, "web", string(rules.ActionStart), result, state.SourceCLIManualStop, state.SourceCLI, true)
	if err != nil {
		t.Fatalf("active postflight sync: %v", err)
	}
	if !change.Changed || change.Action != eventActionMonitor {
		t.Fatalf("active postflight change = %+v", change)
	}
	if !store.active["web"] || store.source["web"] != state.SourceCLI {
		t.Fatalf("store after active postflight start active=%v source=%q", store.active["web"], store.source["web"])
	}

	store.active["web"] = false
	store.source["web"] = state.SourceCLIManualStop
	change, err = SyncManualActionMonitoring(store, "web", string(rules.ActionStart), result, state.SourceCLIManualStop, state.SourceCLI, false)
	if err != nil {
		t.Fatalf("inactive postflight sync: %v", err)
	}
	if change.Changed || store.active["web"] || store.source["web"] != state.SourceCLIManualStop {
		t.Fatalf("inactive postflight should not restore, change=%+v active=%v source=%q", change, store.active["web"], store.source["web"])
	}
}
