package cli

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"sermo/internal/severity"
	"sermo/internal/web"
)

func TestEventTablePreservesUnicode(t *testing.T) {
	if got := eventTableValue("á界🙂x", 3); got != "á界🙂" {
		t.Fatalf("truncated value=%q", got)
	}
	message := strings.Repeat("界", eventsTableMessageWidth+1)
	want := strings.Repeat("界", eventsTableMessageWidth-eventsTableEllipsisWidth) + eventsTableEllipsis
	if got := eventTableMessage(message); got != want {
		t.Fatalf("truncated message=%q", got)
	}
}

func TestEventTableOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event web.Event
		want  string
	}{
		{"completed", web.Event{Kind: "action", Action: "restart", Status: "ok", Severity: "critical"}, "Restart completed"},
		{"blocked", web.Event{Kind: "suppressed", Action: "restart", Status: "blocked"}, "Restart blocked"},
		{"suppressed", web.Event{Kind: "suppressed", Action: "restart"}, "Restart blocked"},
		{"failed", web.Event{Kind: "error", Action: "restart", Status: "failed"}, "Restart failed"},
		{"preflight", web.Event{Kind: "error", Action: "start", Status: "preflight_failed"}, "Start failed"},
		{"dry run", web.Event{Kind: "dry-run", Action: "restart", Severity: "error"}, "Restart · Dry run"},
		{"running", web.Event{Kind: "action", Action: "restart", Status: "running"}, "Restart running"},
		{"no outcome", web.Event{Kind: "action", Action: "restart"}, "Restart"},
		{"unknown", web.Event{Kind: "action", Action: "restart", Status: "pending"}, "Restart · pending"},
		{"warning", web.Event{Kind: "firing", Severity: "warning"}, "Warning"},
		{"critical", web.Event{Kind: "alert", Severity: "critical"}, "Critical"},
		{"default grade", web.Event{Kind: "firing"}, "Error"},
		{"ungraded recovery", web.Event{Kind: "recovered"}, "Recovered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := eventTableFields(tc.event).kind; got != tc.want {
				t.Fatalf("event label = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEventRecoveryKeepsSeverityAsHistory(t *testing.T) {
	for _, level := range []severity.Level{severity.Debug, severity.Info, severity.Warning, severity.Error, severity.Critical} {
		t.Run(level.String(), func(t *testing.T) {
			event := web.Event{
				Kind: "recovered", Severity: level.String(), Rule: "restart-on-change-version",
				Action: "restart", Message: "rule condition recovered",
			}
			row := eventTableFields(event)
			if row.kind != "Recovered" || row.severity != "-" || row.rule != event.Rule {
				t.Fatalf("recovery row = %+v", row)
			}
			for _, want := range []string{"Rule condition cleared", "Previous incident severity: " + level.String(), event.Message} {
				if !strings.Contains(row.message, want) {
					t.Errorf("recovery context %q omits %q", row.message, want)
				}
			}
			var stdout bytes.Buffer
			app := hermeticApp(t, App{Stdout: &stdout})
			want := []web.Event{event}
			app.writeEvents(options{json: true}, "", want)
			var got []web.Event
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("JSON events = %+v, want %+v", got, want)
			}
		})
	}
}
