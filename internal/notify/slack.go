package notify

const (
	slackAttachmentsKey = "attachments"
	slackColorKey       = "color"
	slackFallbackKey    = "fallback"
	slackTitleKey       = "title"
	slackTitleLinkKey   = "title_link"
	slackTextKey        = "text"
	slackMrkdwnInKey    = "mrkdwn_in"
	slackCodeFence      = "```"
)

// buildSlack constructs a Slack incoming-webhook notifier from a config entry.
func buildSlack(name string, entry map[string]any) (Notifier, error) {
	return newWebhookNotifier(TypeSlack, name, entry, slackPayload)
}

// slackPayload renders the Slack incoming-webhook body as one attachment whose
// bar carries the message's colour — its severity, or green for a recovery —
// so a channel reads the grade at a glance. The subject is the title, linked
// to the dashboard row when the daemon has a public URL, and the detail sits in
// a monospace block so the SERMO_* fields stay readable. The fallback is what a
// push notification shows.
func slackPayload(msg Message) []byte {
	attachment := map[string]any{
		slackColorKey:    toneColors[msg.tone()],
		slackFallbackKey: msg.Subject,
		slackTitleKey:    msg.Subject,
	}
	if msg.Link != "" {
		attachment[slackTitleLinkKey] = msg.Link
	}
	if msg.Body != "" {
		attachment[slackTextKey] = slackCodeFence + notifyLF + msg.Body + notifyLF + slackCodeFence
		attachment[slackMrkdwnInKey] = []string{slackTextKey}
	}
	return webhookPayload(map[string]any{slackAttachmentsKey: []any{attachment}})
}
