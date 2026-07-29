package notify

import (
	"context"
	"encoding/json"
	"sermo/internal/telegramapi"
	"strings"
	"testing"
)

func TestBuildTelegramRequiresTokenAndChat(t *testing.T) {
	for _, entry := range []map[string]any{
		{"type": "telegram", "chat_id": "12345"},         // no token
		{"type": "telegram", "token": "123:abc"},         // no chat_id
		{"type": "telegram", "token": "", "chat_id": ""}, // both empty
	} {
		if _, err := buildTelegram("tg", entry); err == nil {
			t.Fatalf("expected error for %v", entry)
		}
	}
}

func TestTelegramSendPostsSendMessage(t *testing.T) {
	var gotURL string
	var gotPayload []byte
	n, err := buildTelegram("tg", map[string]any{
		"type": "telegram", "token": "123:abc", "chat_id": 987654,
	})
	if err != nil {
		t.Fatal(err)
	}
	wn := n.(*webhookNotifier)
	wn.post = capturingPost(t, TypeTelegram, &gotURL, &gotPayload)
	if err := n.Send(context.Background(), Message{Subject: "[sermo] ssh: memory high", Body: "SERMO_SERVICE=ssh"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotURL, telegramapi.APIBase) || !strings.HasSuffix(gotURL, "/"+telegramapi.MethodSendMessage) || !strings.Contains(gotURL, "123:abc") {
		t.Fatalf("posted to %q, want the bot sendMessage URL", gotURL)
	}
	var body struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(gotPayload, &body); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, gotPayload)
	}
	// A numeric chat_id from YAML coerces to its string form.
	if body.ChatID != "987654" || !strings.Contains(body.Text, "memory high") || !strings.Contains(body.Text, "SERMO_SERVICE=ssh") {
		t.Fatalf("unexpected telegram body: %+v", body)
	}
}

func TestTelegramSendIncludesConfiguredOptions(t *testing.T) {
	n, err := buildTelegram("tg", map[string]any{
		"type": "telegram", "token": "123:abc", "chat_id": "1",
		"parse_mode": "MarkdownV2", "silent": true, "message_thread_id": 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	wn := n.(*webhookNotifier)
	var gotURL string
	var gotPayload []byte
	wn.post = capturingPost(t, TypeTelegram, &gotURL, &gotPayload)
	if err := n.Send(context.Background(), Message{Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	var body struct {
		ParseMode           string `json:"parse_mode"`
		DisableNotification bool   `json:"disable_notification"`
		MessageThreadID     int    `json:"message_thread_id"`
	}
	if err := json.Unmarshal(gotPayload, &body); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, gotPayload)
	}
	if body.ParseMode != "MarkdownV2" || !body.DisableNotification || body.MessageThreadID != 42 {
		t.Fatalf("unexpected telegram options: %+v", body)
	}
}

func TestTelegramSendOmitsUnsetOptions(t *testing.T) {
	n, err := buildTelegram("tg", map[string]any{"type": "telegram", "token": "123:abc", "chat_id": "1"})
	if err != nil {
		t.Fatal(err)
	}
	wn := n.(*webhookNotifier)
	var gotURL string
	var gotPayload []byte
	wn.post = capturingPost(t, TypeTelegram, &gotURL, &gotPayload)
	if err := n.Send(context.Background(), Message{Subject: "s"}); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(gotPayload, &body); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, gotPayload)
	}
	// A plain notifier must post exactly the fields it always did.
	for _, k := range []string{telegramapi.FieldParseMode, telegramapi.FieldSilent, telegramapi.FieldThreadID} {
		if _, ok := body[k]; ok {
			t.Fatalf("unset option %q should be omitted, got %v", k, body)
		}
	}
}
