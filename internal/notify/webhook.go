package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sermo/internal/httpx"
	"sermo/internal/netutil"
)

// webhookTimeout bounds a webhook POST so a slow endpoint cannot stall a watch
// cycle.
const webhookTimeout = 15 * time.Second

// webhookErrorSnippetLimit bounds the non-2xx response body included in errors.
const webhookErrorSnippetLimit = 256

const (
	httpStatusClassDivisor = 100
	httpStatusClassSuccess = 2
)

const (
	pushPayloadTitleKey   = "title"
	pushPayloadMessageKey = "message"
)

// Webhook URL prefix constants are the supported webhook transport URL schemes.
const (
	WebhookURLPrefixHTTP  = netutil.URLSchemeHTTP + netutil.URLSchemeSeparator
	WebhookURLPrefixHTTPS = netutil.URLSchemeHTTPS + netutil.URLSchemeSeparator
)

// webhookPoster delivers a JSON payload to a webhook; injected so tests do not
// hit the network. label names the transport in error messages; headers are
// optional extra request headers (e.g. an Authorization token).
type webhookPoster func(ctx context.Context, label, webhook string, headers map[string]string, payload []byte) error

func webhookPayload(v any) []byte {
	b, _ := json.Marshal(v) //nolint:errchkjson // callers pass maps of plain strings, which cannot fail to encode
	return b
}

// pushMessageFields renders the title/message fields shared by Gotify and
// ntfy. A subject-only notification travels as the message without a title.
func pushMessageFields(msg Message) map[string]string {
	fields := map[string]string{pushPayloadMessageKey: msg.Subject}
	if msg.Body != "" {
		fields[pushPayloadTitleKey] = msg.Subject
		fields[pushPayloadMessageKey] = msg.Body
	}
	return fields
}

func sendWebhook(ctx context.Context, post webhookPoster, label, webhook string, headers map[string]string, payload []byte) error {
	if post == nil {
		post = postWebhook
	}
	return post(ctx, label, webhook, headers, payload)
}

// webhookNotifier is the shared shape of the webhook-posting notifiers: a named
// webhook plus the payload renderer that gives each service its body format.
// Uses only net/http (no external dependency).
type webhookNotifier struct {
	name    string
	typ     string
	webhook string
	headers map[string]string // optional extra request headers (auth tokens)
	post    webhookPoster
	payload func(Message) []byte
}

// Name returns the notifier's configured name.
func (n *webhookNotifier) Name() string { return n.name }

// Type returns the notifier type identifier.
func (n *webhookNotifier) Type() string { return n.typ }

// Send posts the rendered message to the configured webhook.
func (n *webhookNotifier) Send(ctx context.Context, msg Message) error {
	return sendWebhook(ctx, n.post, n.typ, n.webhook, n.headers, n.payload(msg))
}

// newWebhookNotifier constructs a webhook notifier from a config entry.
func newWebhookNotifier(typ, name string, entry map[string]any, payload func(Message) []byte) (Notifier, error) {
	webhook, err := webhookURL(typ, entry)
	if err != nil {
		return nil, err
	}
	return &webhookNotifier{name: name, typ: typ, webhook: webhook, payload: payload}, nil
}

// postWebhook POSTs a JSON payload and fails on a non-2xx answer; label names
// the transport in error messages.
func postWebhook(ctx context.Context, label, webhook string, headers map[string]string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(payload))
	if err != nil {
		// url.Parse also returns a *url.Error embedding the raw URL (reachable
		// when a token carries a control char), so scrub this path too.
		return fmt.Errorf("build %s webhook request: %w", label, netutil.URLErrorCause(err))
	}
	req.Header.Set(httpx.HeaderContentType, httpx.ContentTypeJSON)
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	client := &http.Client{Timeout: webhookTimeout}
	resp, err := httpx.Do(client, req)
	if err != nil {
		// A transport error is a *url.Error whose text embeds the full request
		// URL; for Telegram that URL carries the bot token, and this error is
		// surfaced in notify-failed events and the web UI. Report only the
		// underlying cause so no credential ever reaches an event or log.
		return fmt.Errorf("post %s webhook: %w", label, netutil.URLErrorCause(err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/httpStatusClassDivisor != httpStatusClassSuccess {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, webhookErrorSnippetLimit))
		return fmt.Errorf("%s webhook returned %s: %s", label, resp.Status, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// webhookURL reads and validates the `webhook` field shared by the webhook
// transports: required, and an http(s) URL.
func webhookURL(typ string, entry map[string]any) (string, error) {
	webhook, _ := entry[KeyWebhook].(string)
	if webhook == "" {
		return "", errors.New(typ + " notifier requires a webhook")
	}
	if !strings.HasPrefix(webhook, WebhookURLPrefixHTTPS) && !strings.HasPrefix(webhook, WebhookURLPrefixHTTP) {
		return "", errors.New(typ + " webhook must be an http(s) URL")
	}
	return webhook, nil
}
