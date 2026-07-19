package notify

import (
	"context"
	"encoding/json"
	"testing"
)

func TestBuildNtfyRequiresWebhook(t *testing.T) {
	assertBuildWebhookNotifier(t, buildNtfy, "ntfy", "push",
		"https://ntfy.sh/sermo-alerts", "ntfy.sh/sermo-alerts")
}

func TestBuildNtfyRequiresTopic(t *testing.T) {
	for _, webhook := range []string{"https://ntfy.sh", "https://ntfy.sh/"} {
		if _, err := buildNtfy("push", map[string]any{"type": "ntfy", "webhook": webhook}); err == nil {
			t.Fatalf("webhook %q should be rejected (no topic)", webhook)
		}
	}
}

func TestNtfySubpathInstallKeepsPrefix(t *testing.T) {
	base, topic, err := ParseNtfyWebhook("https://host.example.net/ntfy/sermo-alerts")
	if err != nil {
		t.Fatal(err)
	}
	if base != "https://host.example.net/ntfy" || topic != "sermo-alerts" {
		t.Fatalf("subpath parse = base %q topic %q, want the /ntfy prefix kept", base, topic)
	}
}

func TestNtfySendPublishesTopicJSON(t *testing.T) {
	var gotURL string
	var gotHeaders map[string]string
	var gotPayload []byte
	n, err := buildNtfy("push", map[string]any{
		"type": "ntfy", "webhook": "https://ntfy.example.net/sermo-alerts", "token": "tk_secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	wn := n.(*webhookNotifier)
	wn.post = func(_ context.Context, label, url string, headers map[string]string, payload []byte) error {
		if label != TypeNtfy {
			t.Fatalf("label = %q, want ntfy", label)
		}
		gotURL, gotHeaders, gotPayload = url, headers, payload
		return nil
	}
	if err := n.Send(context.Background(), Message{Subject: "[sermo] storage-root: 95% used", Body: "SERMO_PATH=/"}); err != nil {
		t.Fatal(err)
	}
	// Publishing goes to the server root with the topic in the JSON body.
	if gotURL != "https://ntfy.example.net" {
		t.Fatalf("posted to %q, want the server root", gotURL)
	}
	if gotHeaders[ntfyAuthorizationHeader] != "Bearer tk_secret" {
		t.Fatalf("authorization header = %q", gotHeaders[ntfyAuthorizationHeader])
	}
	var body struct {
		Topic   string `json:"topic"`
		Title   string `json:"title"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(gotPayload, &body); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, gotPayload)
	}
	if body.Topic != "sermo-alerts" || body.Title != "[sermo] storage-root: 95% used" || body.Message != "SERMO_PATH=/" {
		t.Fatalf("unexpected ntfy body: %+v", body)
	}
}

func TestNtfySubjectOnlyTravelsAsMessage(t *testing.T) {
	var body map[string]string
	if err := json.Unmarshal(ntfyPayload("alerts", Message{Subject: "recovered"}), &body); err != nil {
		t.Fatal(err)
	}
	if body["message"] != "recovered" {
		t.Fatalf("message = %q, want the subject", body["message"])
	}
	if _, hasTitle := body["title"]; hasTitle {
		t.Fatalf("subject-only notification should omit the title: %v", body)
	}
}
