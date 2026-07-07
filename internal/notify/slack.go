package notify

import (
	"context"
)

const (
	slackPayloadTextKey = "text"
	slackCodeFence      = "```"
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
func (s *Slack) Type() string { return TypeSlack }

// Send posts the message to the configured Slack webhook.
func (s *Slack) Send(ctx context.Context, msg Message) error {
	return sendWebhook(ctx, s.post, TypeSlack, s.webhook, slackPayload(msg))
}

// buildSlack constructs a Slack notifier from a config entry.
func buildSlack(name string, entry map[string]any) (Notifier, error) {
	webhook, err := webhookURL(TypeSlack, entry)
	if err != nil {
		return nil, err
	}
	return &Slack{name: name, webhook: webhook}, nil
}

// slackPayload renders the Slack incoming-webhook body: the subject as the lead
// line and the detail in a monospace block so the SERMO_* fields stay readable.
func slackPayload(msg Message) []byte {
	return webhookPayload(map[string]string{slackPayloadTextKey: slackText(msg)})
}

func slackText(msg Message) string {
	if msg.Body != "" {
		return msg.Subject + "\n" + slackCodeFence + "\n" + msg.Body + "\n" + slackCodeFence
	}
	return msg.Subject
}
