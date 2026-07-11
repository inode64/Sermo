package notify

import "testing"

func TestTestMessage(t *testing.T) {
	msg := TestMessage()
	if msg.Subject != TestSubject || msg.Fields[TestField] != "true" || msg.Body == "" {
		t.Fatalf("TestMessage() = %+v", msg)
	}
}

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

func TestBuildCanSkipTemplates(t *testing.T) {
	raw := map[string]any{
		"ops-email": map[string]any{
			"type": "email", "dsn": "smtp://localhost:25", "from": "sermo@x", "to": []any{"ops@x"}, "template": "default-alert",
		},
	}
	if notifiers, warns := Build(raw); len(notifiers) != 0 || len(warns) == 0 {
		t.Fatalf("template without dir should warn and skip: notifiers=%v warns=%v", notifiers, warns)
	}
	notifiers, warns := Build(raw, WithoutTemplates())
	if len(warns) != 0 {
		t.Fatalf("WithoutTemplates should ignore template warnings, got %v", warns)
	}
	if _, ok := notifiers["ops-email"]; !ok {
		t.Fatalf("ops-email should build when templates are disabled: %v", notifiers)
	}
}

func TestSupportedTypes(t *testing.T) {
	got := SupportedTypes()
	if len(got) != 5 || got[0] != "email" || got[1] != "slack" || got[2] != "teams" || got[3] != "tty" || got[4] != "wall" {
		t.Fatalf("SupportedTypes = %v, want [email slack teams tty wall]", got)
	}
}

func TestBuildTTYRegistry(t *testing.T) {
	notifiers, warns := Build(map[string]any{
		"tty": map[string]any{"type": "tty"},
	})
	if _, ok := notifiers["tty"]; !ok || len(warns) != 0 {
		t.Fatalf("tty notifier should build cleanly: %v %v", notifiers, warns)
	}
}

func TestBuildWallRegistry(t *testing.T) {
	notifiers, warns := Build(map[string]any{
		"wall": map[string]any{"type": "wall"},
	})
	if _, ok := notifiers["wall"]; !ok || len(warns) != 0 {
		t.Fatalf("wall notifier should build cleanly: %v %v", notifiers, warns)
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
