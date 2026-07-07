package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildSlackRequiresWebhook(t *testing.T) {
	good, err := buildSlack("team", map[string]any{"type": "slack", "webhook": "https://hooks.slack.com/services/x"})
	if err != nil {
		t.Fatalf("valid slack: %v", err)
	}
	if s := good.(*Slack); s.Type() != "slack" || s.Name() != "team" {
		t.Fatalf("unexpected slack: %+v", s)
	}
	for _, entry := range []map[string]any{
		{"type": "slack"}, // no webhook
		{"type": "slack", "webhook": "slack.com/x"}, // not an http(s) URL
	} {
		if _, err := buildSlack("n", entry); err == nil {
			t.Fatalf("expected error for %v", entry)
		}
	}
}

func TestSlackSendPostsPayload(t *testing.T) {
	var gotURL string
	var gotPayload []byte
	s := &Slack{
		name:    "team",
		webhook: "https://hooks.slack.com/services/x",
		post: func(_ context.Context, label, url string, payload []byte) error {
			if label != TypeSlack {
				t.Fatalf("label = %q, want slack", label)
			}
			gotURL, gotPayload = url, payload
			return nil
		},
	}
	if err := s.Send(context.Background(), Message{Subject: "[sermo] storage-root: 95% used", Body: "SERMO_PATH=/"}); err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://hooks.slack.com/services/x" {
		t.Fatalf("posted to %q", gotURL)
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(gotPayload, &body); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, gotPayload)
	}
	if !strings.Contains(body.Text, "storage-root") || !strings.Contains(body.Text, "SERMO_PATH=/") {
		t.Fatalf("unexpected slack text: %q", body.Text)
	}
}
