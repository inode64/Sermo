package notify

import (
	"context"
	"slices"
	"testing"
)

// assertBuildWebhookNotifier builds a webhook notifier with a valid URL and
// asserts its identity, then asserts build rejects a missing and a non-http(s)
// webhook.
func assertBuildWebhookNotifier(t *testing.T, build func(name string, entry map[string]any) (Notifier, error), typ, name, goodWebhook, badWebhook string) {
	t.Helper()
	good, err := build(name, map[string]any{"type": typ, "webhook": goodWebhook})
	if err != nil {
		t.Fatalf("valid %s: %v", typ, err)
	}
	if good.Type() != typ || good.Name() != name {
		t.Fatalf("unexpected %s: %+v", typ, good)
	}
	for _, entry := range []map[string]any{
		{"type": typ},                        // no webhook
		{"type": typ, "webhook": badWebhook}, // not an http(s) URL
	} {
		if _, err := build("n", entry); err == nil {
			t.Fatalf("expected error for %v", entry)
		}
	}
}

// capturingPost returns a post func that asserts the label equals wantLabel and
// records the posted url and payload into the given pointers.
func capturingPost(t *testing.T, wantLabel string, url *string, payload *[]byte) func(context.Context, string, string, map[string]string, []byte) error {
	t.Helper()
	return func(_ context.Context, label, gotURL string, _ map[string]string, gotPayload []byte) error {
		t.Helper()
		if label != wantLabel {
			t.Fatalf("label = %q, want %q", label, wantLabel)
		}
		*url, *payload = gotURL, gotPayload
		return nil
	}
}

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
	want := []string{TypeEmail, TypeNtfy, TypeSlack, TypeTeams, TypeTelegram, TypeTTY, TypeWall}
	if !slices.Equal(got, want) {
		t.Fatalf("SupportedTypes = %v, want %v", got, want)
	}
}

func TestBuildLocalRegistry(t *testing.T) {
	for _, typ := range []string{"tty", "wall"} {
		t.Run(typ, func(t *testing.T) {
			notifiers, warns := Build(map[string]any{typ: map[string]any{"type": typ}})
			if _, ok := notifiers[typ]; !ok || len(warns) != 0 {
				t.Fatalf("%s notifier should build cleanly: %v %v", typ, notifiers, warns)
			}
		})
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
