package notify

import (
	"context"
)

// Slack posts notifications to a Slack incoming webhook. Uses only net/http (no
// external dependency).
type Slack struct {
	name    string
	webhook string
	post    webhookPoster
}

// Name returns the notifier's configured name.
func (s *Slack) Name() string { return s.name }

// Type returns the notifier type identifier.
func (s *Slack) Type() string { return "slack" }

// Send posts the message to the configured Slack webhook.
func (s *Slack) Send(ctx context.Context, msg Message) error {
	return webhookPost(s.post, slackPost)(ctx, s.webhook, slackPayload(msg))
}

// buildSlack constructs a Slack notifier from a config entry.
func buildSlack(name string, entry map[string]any) (Notifier, error) {
	webhook, err := webhookURL("slack", entry)
	if err != nil {
		return nil, err
	}
	return &Slack{name: name, webhook: webhook, post: slackPost}, nil
}

// slackPayload renders the Slack incoming-webhook body: the subject as the lead
// line and the detail in a monospace block so the SERMO_* fields stay readable.
func slackPayload(msg Message) []byte {
	return webhookPayload(map[string]string{"text": slackText(msg)})
}

func slackText(msg Message) string {
	if msg.Body != "" {
		return msg.Subject + "\n```\n" + msg.Body + "\n```"
	}
	return msg.Subject
}

func slackPost(ctx context.Context, webhook string, payload []byte) error {
	return postWebhook(ctx, "slack", webhook, payload)
}
