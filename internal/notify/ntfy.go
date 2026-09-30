package notify

import (
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"sermo/internal/cfgval"
	"sermo/internal/httpx"
)

const (
	ntfyTopicKey    = "topic"
	ntfyPriorityKey = "priority"
	ntfyClickKey    = "click"

	ntfyBearerPrefix = "Bearer "

	// ntfyMessageLimit is ntfy's default message-size-limit in bytes.
	ntfyMessageLimit = 4096

	truncatedMarker = "\n… (truncated)"
)

// parseNtfyWebhook splits an ntfy topic URL into the publish base URL and the
// topic name. The topic is the last path segment; any leading segments are a
// reverse-proxy subpath kept on the base (https://host/ntfy/alerts →
// base https://host/ntfy, topic alerts). Publishing POSTs the topic in the
// JSON body to that base, which keeps title and body structured.
func parseNtfyWebhook(webhook string) (base, topic string, err error) {
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
	webhook := webhookURL(entry)
	// Build validates the transport entry before invoking this constructor.
	server, topic, _ := parseNtfyWebhook(webhook)
	var headers map[string]string
	if token := cfgval.String(entry[KeyToken]); token != "" {
		headers = map[string]string{httpx.HeaderAuthorization: ntfyBearerPrefix + token}
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
	fields := pushMessageFields(msg)
	// ntfy delivers a message over its size limit as a file attachment (or
	// rejects it when attachments are disabled), hiding the alert text.
	fields[pushPayloadMessageKey] = truncateBytes(fields[pushPayloadMessageKey], ntfyMessageLimit)
	body := make(map[string]any, len(fields))
	for name, value := range fields {
		body[name] = value
	}
	body[ntfyTopicKey] = topic
	// The severity sets how loudly the phone announces it; a tap opens the
	// dashboard row.
	body[ntfyPriorityKey] = tonePriorities[msg.tone()]
	if msg.Link != "" {
		body[ntfyClickKey] = msg.Link
	}
	return webhookPayload(body)
}

// truncateBytes cuts s to at most limit bytes on a UTF-8 character boundary,
// ending it with truncatedMarker when anything was dropped.
func truncateBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit - len(truncatedMarker)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + truncatedMarker
}
