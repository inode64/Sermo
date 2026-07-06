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
)

// webhookTimeout bounds a webhook POST so a slow endpoint cannot stall a watch
// cycle.
const webhookTimeout = 15 * time.Second

// webhookPoster delivers a JSON payload to a webhook; injected so tests do not
// hit the network. label names the transport in error messages.
type webhookPoster func(ctx context.Context, label, webhook string, payload []byte) error

func webhookPayload(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func sendWebhook(ctx context.Context, post webhookPoster, label, webhook string, payload []byte) error {
	if post == nil {
		post = postWebhook
	}
	return post(ctx, label, webhook, payload)
}

// postWebhook POSTs a JSON payload and fails on a non-2xx answer; label names
// the transport in error messages.
func postWebhook(ctx context.Context, label, webhook string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: webhookTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("%s webhook returned %s: %s", label, resp.Status, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// webhookURL reads and validates the `webhook` field shared by the webhook
// transports: required, and an http(s) URL.
func webhookURL(typ string, entry map[string]any) (string, error) {
	webhook, _ := entry[keyWebhook].(string)
	if webhook == "" {
		return "", errors.New(typ + " notifier requires a webhook")
	}
	if !strings.HasPrefix(webhook, "https://") && !strings.HasPrefix(webhook, "http://") {
		return "", errors.New(typ + " webhook must be an http(s) URL")
	}
	return webhook, nil
}
