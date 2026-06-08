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

// slackTimeout bounds the webhook POST so a slow Slack endpoint cannot stall a
// watch cycle.
const slackTimeout = 15 * time.Second

// slackPoster delivers a JSON payload to a webhook; injected so tests do not hit
// the network.
type slackPoster func(ctx context.Context, webhook string, payload []byte) error

// Slack posts notifications to a Slack incoming webhook. Uses only net/http (no
// external dependency).
type Slack struct {
	name    string
	webhook string
	post    slackPoster
}

// Name returns the notifier's configured name.
func (s *Slack) Name() string { return s.name }

// Type returns the notifier type identifier.
func (s *Slack) Type() string { return "slack" }

// Send posts the message to the configured Slack webhook.
func (s *Slack) Send(ctx context.Context, msg Message) error {
	post := s.post
	if post == nil {
		post = slackPost
	}
	return post(ctx, s.webhook, slackPayload(msg))
}

// buildSlack constructs a Slack notifier from a config entry.
func buildSlack(name string, entry map[string]any) (Notifier, error) {
	webhook, _ := entry["webhook"].(string)
	if webhook == "" {
		return nil, errors.New("slack notifier requires a webhook")
	}
	if !strings.HasPrefix(webhook, "https://") && !strings.HasPrefix(webhook, "http://") {
		return nil, errors.New("slack webhook must be an http(s) URL")
	}
	return &Slack{name: name, webhook: webhook, post: slackPost}, nil
}

// slackPayload renders the Slack incoming-webhook body: the subject as the lead
// line and the detail in a monospace block so the SERMO_* fields stay readable.
func slackPayload(msg Message) []byte {
	text := msg.Subject
	if msg.Body != "" {
		text += "\n```\n" + msg.Body + "\n```"
	}
	b, _ := json.Marshal(map[string]string{"text": text})
	return b
}

func slackPost(ctx context.Context, webhook string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: slackTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("slack webhook returned %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
	}
	return nil
}
