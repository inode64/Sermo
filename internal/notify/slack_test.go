package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildSlackRequiresWebhook(t *testing.T) {
	assertBuildWebhookNotifier(t, "slack", "team",
		"https://hooks.slack.com/services/x", "slack.com/x")
}

func TestSlackSendPostsPayload(t *testing.T) {
	var gotURL string
	var gotPayload []byte
	s := &webhookNotifier{
		name:    "team",
		typ:     TypeSlack,
		webhook: "https://hooks.slack.com/services/x",
		post:    capturingPost(t, TypeSlack, &gotURL, &gotPayload),
		payload: slackPayload,
	}
	if err := s.Send(context.Background(), Message{Subject: "[sermo] storage-root: 95% used", Body: "SERMO_PATH=/"}); err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://hooks.slack.com/services/x" {
		t.Fatalf("posted to %q", gotURL)
	}
	var body struct {
		Attachments []struct {
			Color    string `json:"color"`
			Title    string `json:"title"`
			Fallback string `json:"fallback"`
			Text     string `json:"text"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(gotPayload, &body); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, gotPayload)
	}
	if len(body.Attachments) != 1 {
		t.Fatalf("attachments = %+v, want one", body.Attachments)
	}
	got := body.Attachments[0]
	if !strings.Contains(got.Title, "storage-root") || got.Fallback != got.Title || !strings.Contains(got.Text, "SERMO_PATH=/") {
		t.Fatalf("unexpected slack attachment: %+v", got)
	}
}
