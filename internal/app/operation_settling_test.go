package app

import (
	"fmt"
	"testing"

	"sermo/internal/operation"
	"sermo/internal/state"
)

func TestOperationSettlingLifecycle(t *testing.T) {
	store := newFakeStore()

	if err := beginOperationSettling(store, "web", "restart", state.SourceCLI); err != nil {
		t.Fatalf("begin restart: %v", err)
	}
	rec, found, err := store.OperationSettling("web")
	if err != nil || !found {
		t.Fatalf("operation settling after begin: found=%v err=%v", found, err)
	}
	if rec.Action != "restart" || rec.Phase != state.OperationSettlingRunning || rec.Source != state.SourceCLI {
		t.Fatalf("begin record = %+v", rec)
	}

	result := operation.Result{Service: "web", Action: "restart", Status: operation.ResultOK}
	if err := finishOperationSettling(store, "web", "restart", state.SourceCLI, result, nil); err != nil {
		t.Fatalf("finish restart: %v", err)
	}
	rec, found, err = store.OperationSettling("web")
	if err != nil || !found {
		t.Fatalf("operation settling after finish: found=%v err=%v", found, err)
	}
	if rec.Phase != state.OperationSettlingSettling {
		t.Fatalf("restart should wait for observation, got %+v", rec)
	}

	if err := beginOperationSettling(store, "web", "stop", state.SourceWeb); err != nil {
		t.Fatalf("begin stop: %v", err)
	}
	result = operation.Result{Service: "web", Action: "stop", Status: operation.ResultOK}
	if err := finishOperationSettling(store, "web", "stop", state.SourceWeb, result, nil); err != nil {
		t.Fatalf("finish stop: %v", err)
	}
	if _, found, _ = store.OperationSettling("web"); found {
		t.Fatal("successful stop should clear operation settling")
	}

	if err := beginOperationSettling(store, "web", "start", state.SourceCLI); err != nil {
		t.Fatalf("begin failed start: %v", err)
	}
	result = operation.Result{Service: "web", Action: "start", Status: operation.ResultFailed}
	if err := finishOperationSettling(store, "web", "start", state.SourceCLI, result, fmt.Errorf("failed")); err != nil {
		t.Fatalf("finish failed start: %v", err)
	}
	if _, found, _ = store.OperationSettling("web"); found {
		t.Fatal("failed operation should clear operation settling")
	}

	if err := beginOperationSettling(store, "web", "restart", state.SourceWeb); err != nil {
		t.Fatalf("begin active postflight restart: %v", err)
	}
	result = operation.Result{Service: "web", Action: "restart", Status: operation.ResultPostflightFailed}
	if err := finishOperationSettlingWithActive(store, "web", "restart", state.SourceWeb, result, nil, true); err != nil {
		t.Fatalf("finish active postflight restart: %v", err)
	}
	rec, found, err = store.OperationSettling("web")
	if err != nil || !found {
		t.Fatalf("active postflight restart should keep settling: found=%v err=%v", found, err)
	}
	if rec.Phase != state.OperationSettlingSettling || rec.Source != state.SourceWeb {
		t.Fatalf("active postflight restart record = %+v", rec)
	}

	if err := finishOperationSettlingWithActive(store, "web", "restart", state.SourceWeb, result, nil, false); err != nil {
		t.Fatalf("finish inactive postflight restart: %v", err)
	}
	if _, found, _ = store.OperationSettling("web"); found {
		t.Fatal("inactive postflight restart should clear operation settling")
	}
}
