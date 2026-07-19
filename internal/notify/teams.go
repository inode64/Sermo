package notify

const (
	teamsAdaptiveCardSchema    = "http://adaptivecards.io/schemas/adaptive-card.json"
	teamsAdaptiveCardType      = "AdaptiveCard"
	teamsAdaptiveCardVersion   = "1.4"
	teamsAttachmentContentType = "application/vnd.microsoft.card.adaptive"
	teamsCardBodyKey           = "body"
	teamsCardContentKey        = "content"
	teamsCardContentTypeKey    = "contentType"
	teamsCardSchemaKey         = "$schema"
	teamsCardTypeKey           = "type"
	teamsCardVersionKey        = "version"
	teamsFontTypeKey           = "fontType"
	teamsFontTypeMonospace     = "Monospace"
	teamsMessageAttachmentsKey = "attachments"
	teamsMessageType           = "message"
	teamsMSTeamsKey            = "msteams"
	teamsMSTeamsWidth          = "Full"
	teamsMSTeamsWidthKey       = "width"
	teamsTextBlockType         = "TextBlock"
	teamsTextKey               = "text"
	teamsTextWeightBolder      = "Bolder"
	teamsTextWeightKey         = "weight"
	teamsTextWrapKey           = "wrap"
)

// buildTeams constructs a Microsoft Teams incoming-webhook notifier (a Teams
// Workflows / Power Automate "when a webhook request is received" URL) from a
// config entry.
func buildTeams(name string, entry map[string]any) (Notifier, error) {
	return newWebhookNotifier(TypeTeams, name, entry, teamsPayload)
}

// teamsPayload renders the Teams webhook body as a message with one Adaptive
// Card: the subject as the bold lead line and the detail (the SERMO_* fields)
// in a monospace block — the same layout as the slack payload.
func teamsPayload(msg Message) []byte {
	return webhookPayload(map[string]any{
		teamsCardTypeKey: teamsMessageType,
		teamsMessageAttachmentsKey: []any{map[string]any{
			teamsCardContentTypeKey: teamsAttachmentContentType,
			teamsCardContentKey:     teamsCard(msg),
		}},
	})
}

func teamsCard(msg Message) map[string]any {
	return map[string]any{
		teamsCardSchemaKey:  teamsAdaptiveCardSchema,
		teamsCardTypeKey:    teamsAdaptiveCardType,
		teamsCardVersionKey: teamsAdaptiveCardVersion,
		teamsMSTeamsKey:     map[string]any{teamsMSTeamsWidthKey: teamsMSTeamsWidth},
		teamsCardBodyKey:    teamsCardBody(msg),
	}
}

func teamsCardBody(msg Message) []map[string]any {
	body := []map[string]any{{
		teamsCardTypeKey: teamsTextBlockType, teamsTextKey: msg.Subject, teamsTextWeightKey: teamsTextWeightBolder, teamsTextWrapKey: true,
	}}
	if msg.Body != "" {
		body = append(body, map[string]any{
			teamsCardTypeKey: teamsTextBlockType, teamsTextKey: msg.Body, teamsTextWrapKey: true, teamsFontTypeKey: teamsFontTypeMonospace,
		})
	}
	return body
}
