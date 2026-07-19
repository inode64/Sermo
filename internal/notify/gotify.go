package notify

import (
	"errors"
	"strings"

	"sermo/internal/cfgval"
)

const (
	gotifyMessagePath = "/message"
	gotifyTitleKey    = "title"
	gotifyMessageKey  = "message"
	gotifyKeyHeader   = "X-Gotify-Key"
)

// buildGotify constructs a Gotify notifier from a config entry: `webhook` is
// the server base URL and `token` the application token, sent as the
// X-Gotify-Key header so it stays out of the URL. Self-hosted push with no
// external dependency.
func buildGotify(name string, entry map[string]any) (Notifier, error) {
	webhook, err := webhookURL(TypeGotify, entry)
	if err != nil {
		return nil, err
	}
	token := cfgval.String(entry[KeyToken])
	if token == "" {
		return nil, errors.New("gotify notifier requires a token")
	}
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
	body := map[string]string{gotifyMessageKey: msg.Subject}
	if msg.Body != "" {
		body[gotifyTitleKey] = msg.Subject
		body[gotifyMessageKey] = msg.Body
	}
	return webhookPayload(body)
}
