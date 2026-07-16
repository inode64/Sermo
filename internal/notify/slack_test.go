package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildSlackRequiresWebhook(t *testing.T) {
	assertBuildWebhookNotifier(t, buildSlack, "slack", "team",
		"https://hooks.slack.com/services/x", "slack.com/x")
}

func TestSlackSendPostsPayload(t *testing.T) {
	var gotURL string
	var gotPayload []byte
	s := &Slack{
		name:    "team",
		webhook: "https://hooks.slack.com/services/x",
		post:    capturingPost(t, TypeSlack, &gotURL, &gotPayload),
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
