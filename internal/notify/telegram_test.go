package notify

import (
	"context"
	"encoding/json"
	"sermo/internal/telegramapi"
	"strings"
	"testing"
)

func TestBuildTelegramRequiresTokenAndValidatedChat(t *testing.T) {
	for _, entry := range []map[string]any{
		{"type": "telegram", "chat_id": "12345"},         // no token
		{"type": "telegram", "token": "", "chat_id": ""}, // both empty
	} {
		if _, err := buildTelegram("tg", entry); err == nil {
			t.Fatalf("expected error for %v", entry)
		}
	}
	notifiers, warnings := Build(map[string]any{"tg": map[string]any{KeyType: TypeTelegram, KeyToken: "123:abc"}})
	if len(notifiers) != 0 || len(warnings) == 0 {
		t.Fatalf("missing chat: notifiers=%v warnings=%v", notifiers, warnings)
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

// telegramSentText sends msg through n and returns the posted text and
// parse_mode.
func telegramSentText(t *testing.T, n Notifier, msg Message) (text, parseMode string) {
	t.Helper()
	var gotURL string
	var gotPayload []byte
	inner := n
	if tn, ok := n.(*templatedNotifier); ok {
		inner = tn.inner
	}
	inner.(*webhookNotifier).post = capturingPost(t, TypeTelegram, &gotURL, &gotPayload)
	if err := n.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode"`
	}
	if err := json.Unmarshal(gotPayload, &body); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, gotPayload)
	}
	return body.Text, body.ParseMode
}

func TestTelegramEscapesGeneratedTextForParseMode(t *testing.T) {
	msg := Message{Subject: "[sermo] web: check failed", Body: "SERMO_WATCH=disk-root\nSERMO_OUTPUT=<b> & 1.5"}
	tests := []struct {
		parseMode string
		want      string
	}{
		{parseMode: "MarkdownV2", want: "\\[sermo\\] web: check failed\nSERMO\\_WATCH\\=disk\\-root\nSERMO\\_OUTPUT\\=<b\\> & 1\\.5"},
		{parseMode: "Markdown", want: "\\[sermo] web: check failed\nSERMO\\_WATCH=disk-root\nSERMO\\_OUTPUT=<b> & 1.5"},
		{parseMode: "HTML", want: "[sermo] web: check failed\nSERMO_WATCH=disk-root\nSERMO_OUTPUT=&lt;b&gt; &amp; 1.5"},
	}
	for _, tt := range tests {
		t.Run(tt.parseMode, func(t *testing.T) {
			n, err := buildTelegram("tg", map[string]any{"type": "telegram", "token": "123:abc", "chat_id": "1", "parse_mode": tt.parseMode})
			if err != nil {
				t.Fatal(err)
			}
			text, mode := telegramSentText(t, n, msg)
			if text != tt.want || mode != tt.parseMode {
				t.Fatalf("text = %q (parse_mode %q), want %q", text, mode, tt.want)
			}
		})
	}
}

func TestTelegramTemplateKeepsMarkupAndEscapesValues(t *testing.T) {
	tmpl, err := parseTemplate("tg", []byte(`
subject: '*{{ .Field "SERMO_SERVICE" }}* {{ .Subject }}'
body: '<pre>{{ .Body }}</pre>'
`))
	if err != nil {
		t.Fatal(err)
	}
	msg := Message{
		Subject: "[sermo] php-fpm: cpu high",
		Body:    "SERMO_RULE=cpu_high <x>",
		Fields:  map[string]string{"SERMO_SERVICE": "php-fpm_8.2"},
	}
	for _, tt := range []struct {
		parseMode string
		want      string
	}{
		{parseMode: "MarkdownV2", want: "*php\\-fpm\\_8\\.2* \\[sermo\\] php\\-fpm: cpu high\n<pre>SERMO\\_RULE\\=cpu\\_high <x\\></pre>"},
		{parseMode: "HTML", want: "*php-fpm_8.2* [sermo] php-fpm: cpu high\n<pre>SERMO_RULE=cpu_high &lt;x&gt;</pre>"},
	} {
		t.Run(tt.parseMode, func(t *testing.T) {
			inner, err := buildTelegram("tg", map[string]any{"type": "telegram", "token": "123:abc", "chat_id": "1", "parse_mode": tt.parseMode})
			if err != nil {
				t.Fatal(err)
			}
			text, _ := telegramSentText(t, withTemplate(inner, tmpl), msg)
			if text != tt.want {
				t.Fatalf("text = %q, want %q", text, tt.want)
			}
		})
	}
}

func TestTelegramPlainTextIsNotEscaped(t *testing.T) {
	n, err := buildTelegram("tg", map[string]any{"type": "telegram", "token": "123:abc", "chat_id": "1"})
	if err != nil {
		t.Fatal(err)
	}
	msg := Message{Subject: "[sermo] a_b", Body: "<x> & 1.5"}
	if text, _ := telegramSentText(t, withTemplate(n, &Template{name: "none"}), msg); text != "[sermo] a_b\n<x> & 1.5" {
		t.Fatalf("plain text was altered: %q", text)
	}
}

func TestTelegramTruncatesTextOverTheLimit(t *testing.T) {
	long := Message{Subject: "[sermo] web: check failed", Body: "SERMO_OUTPUT=" + strings.Repeat("output line.\n", 400)}
	for _, parseMode := range []string{"", "MarkdownV2", "HTML"} {
		t.Run("mode="+parseMode, func(t *testing.T) {
			n, err := buildTelegram("tg", map[string]any{"type": "telegram", "token": "123:abc", "chat_id": "1", "parse_mode": parseMode})
			if err != nil {
				t.Fatal(err)
			}
			text, mode := telegramSentText(t, n, long)
			if telegramapi.TextLength(text) > telegramapi.MaxTextLength || !strings.HasSuffix(text, telegramapi.TruncatedMarker) {
				t.Fatalf("text has %d units, want a truncated message within the limit", telegramapi.TextLength(text))
			}
			// Cutting markup could leave an escape or entity open, so an
			// oversized message falls back to the plain text.
			if mode != "" || !strings.HasPrefix(text, "[sermo] web: check failed\nSERMO_OUTPUT=output line.") {
				t.Fatalf("oversized message must be plain text: parse_mode %q, text %q…", mode, text[:40])
			}
		})
	}
}

func TestTelegramTemplateOverTheLimitFallsBackToPlainText(t *testing.T) {
	tmpl, err := parseTemplate("tg", []byte("body: '<pre>{{ .Body }}</pre>'\n"))
	if err != nil {
		t.Fatal(err)
	}
	inner, err := buildTelegram("tg", map[string]any{"type": "telegram", "token": "123:abc", "chat_id": "1", "parse_mode": "HTML"})
	if err != nil {
		t.Fatal(err)
	}
	msg := Message{Subject: "[sermo] db", Body: strings.Repeat("a<b\n", 2000)}
	text, mode := telegramSentText(t, withTemplate(inner, tmpl), msg)
	if mode != "" || strings.Contains(text, "<pre>") || strings.Contains(text, "&lt;") || telegramapi.TextLength(text) > telegramapi.MaxTextLength {
		t.Fatalf("parse_mode %q, %d units, text %q…", mode, telegramapi.TextLength(text), text[:40])
	}
}
