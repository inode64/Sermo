package app

import (
	"errors"
	"testing"

	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/state"
)

func TestOperationSettlingLifecycle(t *testing.T) {
	store := newFakeStore()

	if err := BeginOperationSettling(store, "web", string(rules.ActionRestart)); err != nil {
		t.Fatalf("begin restart: %v", err)
	}
	rec, found, err := store.OperationSettling("web")
	if err != nil || !found {
		t.Fatalf("operation settling after begin: found=%v err=%v", found, err)
	}
	if rec.Phase != state.OperationSettlingRunning {
		t.Fatalf("begin record = %+v", rec)
	}

	result := operation.Result{Service: "web", Action: string(rules.ActionRestart), Status: operation.ResultOK}
	if err := finishOperationSettling(store, "web", string(rules.ActionRestart), result, nil, false); err != nil {
		t.Fatalf("finish restart: %v", err)
	}
	rec, found, err = store.OperationSettling("web")
	if err != nil || !found {
		t.Fatalf("operation settling after finish: found=%v err=%v", found, err)
	}
	if rec.Phase != state.OperationSettlingSettling {
		t.Fatalf("restart should wait for observation, got %+v", rec)
	}

	if err := BeginOperationSettling(store, "web", string(rules.ActionStop)); err != nil {
		t.Fatalf("begin stop: %v", err)
	}
	result = operation.Result{Service: "web", Action: string(rules.ActionStop), Status: operation.ResultOK}
	if err := finishOperationSettling(store, "web", string(rules.ActionStop), result, nil, false); err != nil {
		t.Fatalf("finish stop: %v", err)
	}
	if _, found, _ = store.OperationSettling("web"); found {
		t.Fatal("successful stop should clear operation settling")
	}

	if err := BeginOperationSettling(store, "web", string(rules.ActionStart)); err != nil {
		t.Fatalf("begin failed start: %v", err)
	}
	result = operation.Result{Service: "web", Action: string(rules.ActionStart), Status: operation.ResultFailed}
	if err := finishOperationSettling(store, "web", string(rules.ActionStart), result, errors.New("failed"), false); err != nil {
		t.Fatalf("finish failed start: %v", err)
	}
	if _, found, _ = store.OperationSettling("web"); found {
		t.Fatal("failed operation should clear operation settling")
	}

	if err := BeginOperationSettling(store, "web", string(rules.ActionRestart)); err != nil {
		t.Fatalf("begin active postflight restart: %v", err)
	}
	result = operation.Result{Service: "web", Action: string(rules.ActionRestart), Status: operation.ResultPostflightFailed}
	if err := finishOperationSettling(store, "web", string(rules.ActionRestart), result, nil, true); err != nil {
		t.Fatalf("finish active postflight restart: %v", err)
	}
	rec, found, err = store.OperationSettling("web")
	if err != nil || !found {
		t.Fatalf("active postflight restart should keep settling: found=%v err=%v", found, err)
	}
	if rec.Phase != state.OperationSettlingSettling {
		t.Fatalf("active postflight restart record = %+v", rec)
	}

	if err := finishOperationSettling(store, "web", string(rules.ActionRestart), result, nil, false); err != nil {
		t.Fatalf("finish inactive postflight restart: %v", err)
	}
	if _, found, _ = store.OperationSettling("web"); found {
		t.Fatal("inactive postflight restart should clear operation settling")
	}
}

func TestRepairOperationSettlesLikeStart(t *testing.T) {
	store := newFakeStore()
	result := operation.Result{Service: "web", Action: operation.ActionRepair, Status: operation.ResultOK}

	if err := BeginOperationSettling(store, "web", operation.ActionRepair); err != nil {
		t.Fatalf("begin repair: %v", err)
	}
	rec, found, err := store.OperationSettling("web")
	if err != nil || !found || rec.Phase != state.OperationSettlingRunning {
		t.Fatalf("running repair settling = %+v found=%v err=%v", rec, found, err)
	}
	if err := finishOperationSettling(store, "web", operation.ActionRepair, result, nil, false); err != nil {
		t.Fatalf("finish repair: %v", err)
	}
	rec, found, err = store.OperationSettling("web")
	if err != nil || !found || rec.Phase != state.OperationSettlingSettling {
		t.Fatalf("settled repair = %+v found=%v err=%v", rec, found, err)
	}
}
