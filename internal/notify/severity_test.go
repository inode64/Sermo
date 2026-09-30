package notify

import (
	"context"
	"strings"
	"testing"

	"sermo/internal/severity"
)

func TestMinSeverityValidation(t *testing.T) {
	entry := func(level any) map[string]any {
		return map[string]any{"type": TypeSlack, "webhook": "https://hooks.slack.com/services/T/B/X", KeyMinSeverity: level}
	}
	for _, level := range []string{"debug", "info", "warning", "error", "critical"} {
		if issues := ValidateEntry(entry(level)); len(issues) != 0 {
			t.Errorf("min_severity %q rejected: %v", level, issues)
		}
	}
	for _, bad := range []any{"urgent", "ok", 3} {
		issues := ValidateEntry(entry(bad))
		if len(issues) != 1 || issues[0].Field != KeyMinSeverity || !strings.Contains(issues[0].Suffix, severity.Summary) {
			t.Errorf("min_severity %v = %v, want one issue naming the levels", bad, issues)
		}
	}
	if _, warns := Build(map[string]any{"ops": entry("urgent")}); len(warns) != 1 || !strings.Contains(warns[0], KeyMinSeverity) {
		t.Fatalf("Build warnings = %v, want the invalid min_severity named", warns)
	}
}

func TestBuildCarriesMinSeverity(t *testing.T) {
	notifiers, warns := Build(map[string]any{
		"oncall": map[string]any{"type": TypeSlack, "webhook": "https://hooks.slack.com/services/T/B/X", KeyMinSeverity: "critical"},
		"all":    map[string]any{"type": TypeSlack, "webhook": "https://hooks.slack.com/services/T/B/Y"},
		"debug":  map[string]any{"type": TypeSlack, "webhook": "https://hooks.slack.com/services/T/B/Z", KeyMinSeverity: "debug"},
	})
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	oncall := notifiers["oncall"]
	if oncall.Name() != "oncall" || oncall.Type() != TypeSlack {
		t.Fatalf("wrapped notifier lost its identity: %s/%s", oncall.Name(), oncall.Type())
	}
	if Accepts(oncall, severity.Error) || !Accepts(oncall, severity.Critical) {
		t.Fatal("a critical-only notifier must accept critical and refuse error")
	}
	// Without a floor — or with the debug floor, which filters nothing — the
	// notifier is not wrapped and accepts everything.
	for _, name := range []string{"all", "debug"} {
		if _, wrapped := notifiers[name].(severityFloorNotifier); wrapped || !Accepts(notifiers[name], severity.Debug) {
			t.Fatalf("notifier %s must accept everything unwrapped", name)
		}
	}
}

func TestAccepts(t *testing.T) {
	floor := func(level severity.Level) Notifier {
		return severityFloorNotifier{Notifier: typedTestNotifier{name: "n", typ: TypeSlack}, floor: level}
	}
	tests := []struct {
		floor, level severity.Level
		want         bool
	}{
		{severity.Warning, severity.Info, false},
		{severity.Warning, severity.Warning, true},
		{severity.Warning, severity.Critical, true},
		// An ungraded message is an error.
		{severity.Error, "", true},
		{severity.Critical, "", false},
	}
	for _, tt := range tests {
		if got := Accepts(floor(tt.floor), tt.level); got != tt.want {
			t.Errorf("floor %s accepts %q = %v, want %v", tt.floor, tt.level, got, tt.want)
		}
	}
	if !Accepts(typedTestNotifier{name: "plain"}, severity.Debug) {
		t.Fatal("a notifier without a floor must accept everything")
	}
}

// The floor never filters an explicit send: the notifier test and the
// inventory report call Send directly and must always go out.
func TestSeverityFloorSendDelegates(t *testing.T) {
	inner := &recordingNotifier{}
	n := severityFloorNotifier{Notifier: inner, floor: severity.Critical}
	if err := n.Send(context.Background(), Message{Subject: "test", Severity: severity.Info}); err != nil {
		t.Fatal(err)
	}
	if inner.msg.Subject != "test" {
		t.Fatalf("Send delivered %+v, want the message passed through", inner.msg)
	}
}
