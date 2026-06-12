package notify

import "testing"

func TestBuildRegistry(t *testing.T) {
	notifiers, warns := Build(map[string]any{
		"ops-email": map[string]any{
			"type": "email", "dsn": "smtp://localhost:25", "from": "sermo@x", "to": []any{"ops@x"},
		},
		"bad-type":  map[string]any{"type": "carrier-pigeon"},
		"not-a-map": "nope",
		"broken":    map[string]any{"type": "email"}, // missing dsn/from/to
	})
	if _, ok := notifiers["ops-email"]; !ok {
		t.Fatalf("ops-email should build, got %v", notifiers)
	}
	if len(notifiers) != 1 {
		t.Fatalf("only the valid notifier should build, got %d", len(notifiers))
	}
	if len(warns) != 3 {
		t.Fatalf("expected 3 warnings (bad-type, not-a-map, broken), got %v", warns)
	}
}

func TestBuildEmptyIsNoop(t *testing.T) {
	notifiers, warns := Build(nil)
	if len(notifiers) != 0 || len(warns) != 0 {
		t.Fatalf("empty config should yield nothing: %v %v", notifiers, warns)
	}
}

func TestBuildSkipsDisabledNotifier(t *testing.T) {
	notifiers, warns := Build(map[string]any{
		"ops-email": map[string]any{
			"enabled": false,
		},
	})
	if len(warns) != 0 {
		t.Fatalf("disabled notifier should not warn, got %v", warns)
	}
	if len(notifiers) != 0 {
		t.Fatalf("disabled notifier should not be built, got %v", notifiers)
	}
}

func TestSupportedTypes(t *testing.T) {
	got := SupportedTypes()
	if len(got) != 3 || got[0] != "email" || got[1] != "slack" || got[2] != "teams" {
		t.Fatalf("SupportedTypes = %v, want [email slack teams]", got)
	}
}

func TestBuildSlackRegistry(t *testing.T) {
	notifiers, warns := Build(map[string]any{
		"team-slack": map[string]any{"type": "slack", "webhook": "https://hooks.slack.com/services/x"},
	})
	if _, ok := notifiers["team-slack"]; !ok || len(warns) != 0 {
		t.Fatalf("slack notifier should build cleanly: %v %v", notifiers, warns)
	}
}
