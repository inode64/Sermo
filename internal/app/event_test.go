package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"sermo/internal/operation"
	"sermo/internal/rules"
)

func TestOperationEventEmitter(t *testing.T) {
	var events []Event
	emit := operationEventEmitter(func(e Event) { events = append(events, e) })

	emit(operation.Result{Service: "web", Action: string(rules.ActionRestart), Status: operation.ResultOK, Message: "restart ok"})
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Kind != eventKindAction || events[0].Status != eventStatusOK || events[0].Rule != "" {
		t.Fatalf("ok action = %+v", events[0])
	}

	emit(operation.Result{Service: "web", Action: string(rules.ActionStop), Status: operation.ResultBlocked, Message: "blocked by lock"})
	if events[1].Kind != eventKindSuppressed {
		t.Fatalf("blocked = %+v, want kind=suppressed", events[1])
	}

	emit(operation.Result{Service: "web", Action: string(rules.ActionRestart), Status: operation.ResultFailed, Message: "systemctl failed"})
	emit(operation.Result{Service: "web", Action: string(rules.ActionStart), Status: operation.ResultPreflightFailed, Message: "storage check failed"})
	emit(operation.Result{Service: "web", Action: string(rules.ActionRestart), Status: operation.ResultPostflightFailed, Message: "tcp check failed"})
	emit(operation.Result{Service: "web", Action: string(rules.ActionStop), Status: operation.ResultOrphanProcesses, Message: "residual remains"})
	for i, status := range []operation.ResultStatus{
		operation.ResultFailed,
		operation.ResultPreflightFailed,
		operation.ResultPostflightFailed,
		operation.ResultOrphanProcesses,
	} {
		if events[2+i].Kind != eventKindError || events[2+i].Status != string(status) {
			t.Fatalf("status %q event = %+v, want kind=error", status, events[2+i])
		}
	}

	if operationEventEmitter(nil) != nil {
		t.Fatal("nil emit should yield nil adapter")
	}
}

func TestSlogEmitterLogsHookAtInfo(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	emit := SlogEmitter(logger)

	emit(Event{Watch: "storage-root", Kind: eventKindHook, Message: "fired"})

	out := buf.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "watch=storage-root") {
		t.Fatalf("hook event not logged at info with watch attr: %q", out)
	}
}

func TestSlogEmitterSeverityPerKind(t *testing.T) {
	// Failed watch actions must be visible at the daemon's default Info level;
	// expand-failed and kill-failed used to fall through to Debug.
	cases := []struct {
		kind string
		want string
	}{
		{eventKindExpandFailed, "level=ERROR"},
		{eventKindKillFailed, "level=ERROR"},
		{eventKindExpand, "level=INFO"},
		{eventKindKill, "level=INFO"},
		{eventKindReload, "level=INFO"},
		{eventKindPanicSuppressed, "level=INFO"},
		{eventKindNotifySuppressed, "level=INFO"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			SlogEmitter(logger)(Event{Watch: "w", Kind: tc.kind, Message: "x"})
			if !strings.Contains(buf.String(), tc.want) {
				t.Fatalf("kind %s logged as %q, want %s", tc.kind, buf.String(), tc.want)
			}
		})
	}
}
