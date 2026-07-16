package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildTeamsRequiresWebhook(t *testing.T) {
	assertBuildWebhookNotifier(t, buildTeams, "teams", "ops",
		"https://prod-01.westeurope.logic.azure.com/workflows/x", "logic.azure.com/x")
}

func TestTeamsSendPostsAdaptiveCard(t *testing.T) {
	var gotURL string
	var gotPayload []byte
	n := &Teams{
		name:    "ops",
		webhook: "https://prod-01.westeurope.logic.azure.com/workflows/x",
		post:    capturingPost(t, TypeTeams, &gotURL, &gotPayload),
	}
	if err := n.Send(context.Background(), Message{Subject: "[sermo] storage-root: 95% used", Body: "SERMO_PATH=/"}); err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://prod-01.westeurope.logic.azure.com/workflows/x" {
		t.Fatalf("posted to %q", gotURL)
	}

	var body struct {
		Type        string `json:"type"`
		Attachments []struct {
			ContentType string `json:"contentType"`
			Content     struct {
				Type string `json:"type"`
				Body []struct {
					Text string `json:"text"`
				} `json:"body"`
			} `json:"content"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(gotPayload, &body); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, gotPayload)
	}
	if body.Type != "message" || len(body.Attachments) != 1 {
		t.Fatalf("unexpected envelope: %s", gotPayload)
	}
	att := body.Attachments[0]
	if att.ContentType != "application/vnd.microsoft.card.adaptive" || att.Content.Type != "AdaptiveCard" {
		t.Fatalf("unexpected card: %s", gotPayload)
	}
	if len(att.Content.Body) != 2 ||
		!strings.Contains(att.Content.Body[0].Text, "storage-root") ||
		!strings.Contains(att.Content.Body[1].Text, "SERMO_PATH=/") {
		t.Fatalf("unexpected card body: %s", gotPayload)
	}
}

func TestTeamsPayloadOmitsEmptyBody(t *testing.T) {
	var card struct {
		Attachments []struct {
			Content struct {
				Body []any `json:"body"`
			} `json:"content"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(teamsPayload(Message{Subject: "subject only"}), &card); err != nil {
		t.Fatal(err)
	}
	if len(card.Attachments[0].Content.Body) != 1 {
		t.Fatalf("empty Body must yield a single TextBlock, got %d", len(card.Attachments[0].Content.Body))
	}
}
