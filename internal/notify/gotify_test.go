package notify

import (
	"context"
	"encoding/json"
	"testing"
)

func TestBuildGotifyRequiresWebhookAndToken(t *testing.T) {
	for _, entry := range []map[string]any{
		{"type": "gotify", "token": "A.secret"},                          // no webhook
		{"type": "gotify", "webhook": "https://push.example.net"},        // no token
		{"type": "gotify", "webhook": "push.example.net", "token": "A."}, // not http(s)
	} {
		if _, err := buildGotify("push", entry); err == nil {
			t.Fatalf("expected error for %v", entry)
		}
	}
}

func TestGotifySendPostsMessageWithKeyHeader(t *testing.T) {
	var gotURL string
	var gotHeaders map[string]string
	var gotPayload []byte
	n, err := buildGotify("push", map[string]any{
		"type": "gotify", "webhook": "https://push.example.net/", "token": "A.secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	wn := n.(*webhookNotifier)
	wn.post = func(_ context.Context, label, url string, headers map[string]string, payload []byte) error {
		if label != TypeGotify {
			t.Fatalf("label = %q, want gotify", label)
		}
		gotURL, gotHeaders, gotPayload = url, headers, payload
		return nil
	}
	if err := n.Send(context.Background(), Message{Subject: "[sermo] disk full", Body: "SERMO_PATH=/"}); err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://push.example.net/message" {
		t.Fatalf("posted to %q, want the /message endpoint", gotURL)
	}
	// The app token travels as a header, never inside the URL.
	if gotHeaders[gotifyKeyHeader] != "A.secret" {
		t.Fatalf("key header = %q", gotHeaders[gotifyKeyHeader])
	}
	var body struct {
		Title   string `json:"title"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(gotPayload, &body); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, gotPayload)
	}
	if body.Title != "[sermo] disk full" || body.Message != "SERMO_PATH=/" {
		t.Fatalf("unexpected gotify body: %+v", body)
	}
}
