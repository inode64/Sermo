package notify

import "testing"

func TestConfigSummary(t *testing.T) {
	if got := ConfigSummary("email", map[string]any{"to": []any{"ops@example.com", "oncall@example.com"}}); got != "ops@example.com (+1)" {
		t.Fatalf("email summary = %q", got)
	}
	if got := ConfigSummary("slack", map[string]any{"webhook": "https://hooks.slack.com/services/T/B/X"}); got != "hooks.slack.com" {
		t.Fatalf("slack summary = %q", got)
	}
}
