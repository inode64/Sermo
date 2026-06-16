package notify

import (
	"context"
	"encoding/json"
)

// Teams posts notifications to a Microsoft Teams incoming webhook (a Teams
// Workflows / Power Automate "when a webhook request is received" URL). Uses
// only net/http (no external dependency).
type Teams struct {
	name    string
	webhook string
	post    webhookPoster
}

// Name returns the notifier's configured name.
func (t *Teams) Name() string { return t.name }

// Type returns the notifier type identifier.
func (t *Teams) Type() string { return "teams" }

// Send posts the message to the configured Teams webhook.
func (t *Teams) Send(ctx context.Context, msg Message) error {
	return webhookPost(t.post, teamsPost)(ctx, t.webhook, teamsPayload(msg))
}

// buildTeams constructs a Teams notifier from a config entry.
func buildTeams(name string, entry map[string]any) (Notifier, error) {
	webhook, err := webhookURL("teams", entry)
	if err != nil {
		return nil, err
	}
	return &Teams{name: name, webhook: webhook, post: teamsPost}, nil
}

// teamsPayload renders the Teams webhook body as a message with one Adaptive
// Card: the subject as the bold lead line and the detail (the SERMO_* fields)
// in a monospace block — the same layout as the slack payload.
func teamsPayload(msg Message) []byte {
	body := []map[string]any{{
		"type": "TextBlock", "text": msg.Subject, "weight": "Bolder", "wrap": true,
	}}
	if msg.Body != "" {
		body = append(body, map[string]any{
			"type": "TextBlock", "text": msg.Body, "wrap": true, "fontType": "Monospace",
		})
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"msteams": map[string]any{"width": "Full"},
		"body":    body,
	}
	b, _ := json.Marshal(map[string]any{
		"type": "message",
		"attachments": []any{map[string]any{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"content":     card,
		}},
	})
	return b
}

func teamsPost(ctx context.Context, webhook string, payload []byte) error {
	return postWebhook(ctx, "teams", webhook, payload)
}
