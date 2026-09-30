package notify

import (
	"strings"

	"sermo/internal/cfgval"
)

const (
	gotifyMessagePath   = "/message"
	gotifyKeyHeader     = "X-Gotify-Key"
	gotifyPriorityKey   = "priority"
	gotifyExtrasKey     = "extras"
	gotifyNotificationX = "client::notification"
	gotifyClickKey      = "click"
	gotifyURLKey        = "url"
	// gotifyPriorityScale maps the 1..5 severity priority onto Gotify's 0..10.
	gotifyPriorityScale = 2
)

// buildGotify constructs a Gotify notifier from a config entry: `webhook` is
// the server base URL and `token` the application token, sent as the
// X-Gotify-Key header so it stays out of the URL. Self-hosted push with no
// external dependency.
func buildGotify(name string, entry map[string]any) (Notifier, error) {
	webhook := webhookURL(entry)
	token := cfgval.String(entry[KeyToken])
	return &webhookNotifier{
		name:    name,
		typ:     TypeGotify,
		webhook: strings.TrimRight(webhook, "/") + gotifyMessagePath,
		headers: map[string]string{gotifyKeyHeader: token},
		payload: gotifyPayload,
	}, nil
}

// gotifyPayload renders the Gotify message body: the subject as the title and
// the detail (the SERMO_* fields) as the message. A subject-only notification
// travels as the message alone.
func gotifyPayload(msg Message) []byte {
	fields := pushMessageFields(msg)
	body := make(map[string]any, len(fields))
	for name, value := range fields {
		body[name] = value
	}
	// The severity sets how loudly the client announces it; a tap opens the
	// dashboard row.
	body[gotifyPriorityKey] = tonePriorities[msg.tone()] * gotifyPriorityScale
	if msg.Link != "" {
		body[gotifyExtrasKey] = map[string]any{
			gotifyNotificationX: map[string]any{gotifyClickKey: map[string]any{gotifyURLKey: msg.Link}},
		}
	}
	return webhookPayload(body)
}
