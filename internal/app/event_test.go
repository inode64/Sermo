package app

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/severity"
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

func TestOperationEventRecordUsesCanonicalResultMapping(t *testing.T) {
	record := OperationEventRecord(operation.Result{
		Service: "web",
		Action:  string(rules.ActionRestart),
		Status:  operation.ResultPostflightFailed,
		Message: "tcp check failed",
	})

	if record.Service != "web" || record.Kind != eventKindError || record.Action != string(rules.ActionRestart) ||
		record.Status != string(operation.ResultPostflightFailed) || record.Message != "tcp check failed" {
		t.Fatalf("record = %+v", record)
	}
}

func TestCascadeEventRecordUsesCanonicalRelationshipMapping(t *testing.T) {
	record := CascadeEventRecord("web", operation.Result{
		Service: "db", Action: string(rules.ActionRestart), Status: operation.ResultFailed,
	})
	if record.Service != "db" || record.Kind != eventKindCascade || record.Action != string(rules.ActionRestart) ||
		record.Status != string(operation.ResultFailed) || record.Message != "cascade from web" {
		t.Fatalf("record = %+v", record)
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
		kind  string
		level severity.Level
		want  string
	}{
		{eventKindExpandFailed, "", "level=ERROR"},
		{eventKindKillFailed, "", "level=ERROR"},
		{eventKindMakeStepFailed, "", "level=ERROR"},
		{eventKindError, "", "level=ERROR"},
		// A graded event logs at its grade: an advisory between an outage and
		// routine traffic, critical as an error.
		{eventKindFiring, severity.Critical, "level=ERROR"},
		{eventKindFiring, severity.Error, "level=ERROR"},
		{eventKindFiring, severity.Warning, "level=WARN"},
		{eventKindFiring, severity.Info, "level=INFO"},
		{eventKindFiring, severity.Debug, "level=DEBUG"},
		// A failed action stays an error whatever the incident's grade; an
		// error graded by an advisory incident (its check went unavailable)
		// logs at that grade.
		{eventKindHookFail, severity.Warning, "level=ERROR"},
		{eventKindError, severity.Warning, "level=WARN"},
		// Routine traffic stays at info even inside a critical incident: a
		// recovery or a repair that worked must not page anyone.
		{eventKindRecovered, severity.Critical, "level=INFO"},
		{eventKindAction, severity.Critical, "level=INFO"},
		{eventKindDryRun, severity.Error, "level=INFO"},
		{eventKindSuppressed, severity.Critical, "level=INFO"},
		{eventKindExpand, "", "level=INFO"},
		{eventKindMakeStep, "", "level=INFO"},
		{eventKindMakeStepSkipped, "", "level=INFO"},
		{eventKindKill, "", "level=INFO"},
		{eventKindReload, "", "level=INFO"},
		{eventKindPanicSuppressed, "", "level=INFO"},
		{eventKindNotifySuppressed, "", "level=INFO"},
	}
	for _, tc := range cases {
		t.Run(tc.kind+"/"+tc.level.String(), func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			SlogEmitter(logger)(Event{Watch: "w", Kind: tc.kind, Severity: tc.level, Message: "x"})
			if !strings.Contains(buf.String(), tc.want) {
				t.Fatalf("kind %s severity %q logged as %q, want %s", tc.kind, tc.level, buf.String(), tc.want)
			}
			if tc.level.Valid() && !strings.Contains(buf.String(), "severity="+tc.level.String()) {
				t.Fatalf("graded event lacks its severity attribute: %q", buf.String())
			}
		})
	}
}
