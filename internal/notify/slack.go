package notify

const (
	slackPayloadTextKey = "text"
	slackCodeFence      = "```"
)

// buildSlack constructs a Slack incoming-webhook notifier from a config entry.
func buildSlack(name string, entry map[string]any) (Notifier, error) {
	return newWebhookNotifier(TypeSlack, name, entry, slackPayload)
}

// slackPayload renders the Slack incoming-webhook body: the subject as the lead
// line and the detail in a monospace block so the SERMO_* fields stay readable.
func slackPayload(msg Message) []byte {
	return webhookPayload(map[string]string{slackPayloadTextKey: slackText(msg)})
}

func slackText(msg Message) string {
	if msg.Body != "" {
		return msg.Subject + notifyLF + slackCodeFence + notifyLF + msg.Body + notifyLF + slackCodeFence
	}
	return msg.Subject
}
