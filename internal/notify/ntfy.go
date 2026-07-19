package notify

import (
	"errors"
	"net/url"
	"strings"

	"sermo/internal/cfgval"
)

const (
	ntfyTopicKey   = "topic"
	ntfyTitleKey   = "title"
	ntfyMessageKey = "message"

	ntfyAuthorizationHeader = "Authorization"
	ntfyBearerPrefix        = "Bearer "
)

// ParseNtfyWebhook splits an ntfy topic URL into the publish base URL and the
// topic name. The topic is the last path segment; any leading segments are a
// reverse-proxy subpath kept on the base (https://host/ntfy/alerts →
// base https://host/ntfy, topic alerts). Publishing POSTs the topic in the
// JSON body to that base, which keeps title and body structured.
func ParseNtfyWebhook(webhook string) (base, topic string, err error) {
	u, err := url.Parse(webhook)
	if err != nil || u.Host == "" {
		return "", "", errors.New("ntfy webhook must be a full topic URL (https://server/topic)")
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	topic = segments[len(segments)-1]
	if topic == "" {
		return "", "", errors.New("ntfy webhook must name a topic (https://server/topic)")
	}
	base = u.Scheme + "://" + u.Host
	if prefix := segments[:len(segments)-1]; len(prefix) > 0 {
		base += "/" + strings.Join(prefix, "/")
	}
	return base, topic, nil
}

// buildNtfy constructs an ntfy notifier from a config entry: `webhook` is the
// topic URL and the optional `token` authenticates against a protected topic
// via the Authorization header. Self-hosted push with no external dependency.
func buildNtfy(name string, entry map[string]any) (Notifier, error) {
	webhook, err := webhookURL(TypeNtfy, entry)
	if err != nil {
		return nil, err
	}
	server, topic, err := ParseNtfyWebhook(webhook)
	if err != nil {
		return nil, err
	}
	var headers map[string]string
	if token := cfgval.String(entry[KeyToken]); token != "" {
		headers = map[string]string{ntfyAuthorizationHeader: ntfyBearerPrefix + token}
	}
	return &webhookNotifier{
		name:    name,
		typ:     TypeNtfy,
		webhook: server,
		headers: headers,
		payload: func(msg Message) []byte { return ntfyPayload(topic, msg) },
	}, nil
}

// ntfyPayload renders the ntfy JSON publish body: the subject as the
// notification title and the detail (the SERMO_* fields) as the message. A
// subject-only notification travels as the message alone.
func ntfyPayload(topic string, msg Message) []byte {
	body := map[string]string{ntfyTopicKey: topic, ntfyMessageKey: msg.Subject}
	if msg.Body != "" {
		body[ntfyTitleKey] = msg.Subject
		body[ntfyMessageKey] = msg.Body
	}
	return webhookPayload(body)
}
