package notify

import (
	"errors"

	"sermo/internal/cfgval"
)

const (
	telegramAPIBase         = "https://api.telegram.org/bot"
	telegramSendMessagePath = "/sendMessage"
	telegramChatIDKey       = "chat_id"
	telegramTextKey         = "text"
)

// buildTelegram constructs a Telegram bot notifier from a config entry:
// `token` is the bot token (kept inside the API URL, never surfaced) and
// `chat_id` the numeric chat or `@channel` target.
func buildTelegram(name string, entry map[string]any) (Notifier, error) {
	token := cfgval.String(entry[KeyToken])
	if token == "" {
		return nil, errors.New("telegram notifier requires a token")
	}
	chatID := cfgval.String(entry[KeyChatID])
	if chatID == "" {
		return nil, errors.New("telegram notifier requires a chat_id")
	}
	return &webhookNotifier{
		name:    name,
		typ:     TypeTelegram,
		webhook: telegramAPIBase + token + telegramSendMessagePath,
		payload: func(msg Message) []byte { return telegramPayload(chatID, msg) },
	}, nil
}

// telegramPayload renders the sendMessage body: the subject as the lead line
// and the detail (the SERMO_* fields) below it, as plain text.
func telegramPayload(chatID string, msg Message) []byte {
	text := msg.Subject
	if msg.Body != "" {
		text = msg.Subject + notifyLF + msg.Body
	}
	return webhookPayload(map[string]string{telegramChatIDKey: chatID, telegramTextKey: text})
}
