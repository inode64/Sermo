package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"sermo/internal/operation"
)

func TestOperationEventEmitter(t *testing.T) {
	var events []Event
	emit := operationEventEmitter(func(e Event) { events = append(events, e) })

	emit(operation.Result{Service: "web", Action: "restart", Status: operation.ResultOK, Message: "restart ok"})
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Kind != "action" || events[0].Status != "ok" || events[0].Rule != "" {
		t.Fatalf("ok action = %+v", events[0])
	}

	emit(operation.Result{Service: "web", Action: "stop", Status: operation.ResultBlocked, Message: "blocked by lock"})
	if events[1].Kind != "suppressed" {
		t.Fatalf("blocked = %+v, want kind=suppressed", events[1])
	}

	emit(operation.Result{Service: "web", Action: "restart", Status: operation.ResultFailed, Message: "systemctl failed"})
	emit(operation.Result{Service: "web", Action: "start", Status: operation.ResultPreflightFailed, Message: "storage check failed"})
	emit(operation.Result{Service: "web", Action: "restart", Status: operation.ResultPostflightFailed, Message: "tcp check failed"})
	emit(operation.Result{Service: "web", Action: "stop", Status: operation.ResultOrphanProcesses, Message: "residual remains"})
	for i, status := range []operation.ResultStatus{
		operation.ResultFailed,
		operation.ResultPreflightFailed,
		operation.ResultPostflightFailed,
		operation.ResultOrphanProcesses,
	} {
		if events[2+i].Kind != "error" || events[2+i].Status != string(status) {
			t.Fatalf("status %q event = %+v, want kind=error", status, events[2+i])
		}
	}

	if operationEventEmitter(nil) != nil {
		t.Fatal("nil emit should yield nil adapter")
	}
}

func TestEventKindForResult(t *testing.T) {
	cases := []struct {
		status operation.ResultStatus
		want   string
	}{
		{operation.ResultOK, "action"},
		{operation.ResultBlocked, "suppressed"},
		{operation.ResultFailed, "error"},
		{operation.ResultPreflightFailed, "error"},
		{operation.ResultPostflightFailed, "error"},
		{operation.ResultOrphanProcesses, "error"},
	}
	for _, tc := range cases {
		if got := eventKindForResult(operation.Result{Status: tc.status}); got != tc.want {
			t.Errorf("status %q: got kind %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestSlogEmitterLogsHookAtInfo(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	emit := SlogEmitter(logger)

	emit(Event{Watch: "disk-root", Kind: "hook", Message: "fired"})

	out := buf.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "watch=disk-root") {
		t.Fatalf("hook event not logged at info with watch attr: %q", out)
	}
}
