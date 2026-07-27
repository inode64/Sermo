package notify

import (
	"errors"
	"slices"

	"sermo/internal/cfgval"
)

const (
	telegramAPIBase         = "https://api.telegram.org/bot"
	telegramSendMessagePath = "/sendMessage"
	telegramChatIDKey       = "chat_id"
	telegramTextKey         = "text"
	telegramParseModeKey    = "parse_mode"
	telegramSilentKey       = "disable_notification"
	telegramThreadIDKey     = "message_thread_id"
)

// telegramParseModes are the Bot API `parse_mode` values Sermo accepts, sorted.
var telegramParseModes = []string{"HTML", "Markdown", "MarkdownV2"}

// TelegramParseModes returns the accepted `parse_mode` values, for validation
// and docs.
func TelegramParseModes() []string { return slices.Clone(telegramParseModes) }

// ValidTelegramParseMode reports whether s is an accepted `parse_mode`.
func ValidTelegramParseMode(s string) bool { return slices.Contains(telegramParseModes, s) }

// telegramOptions carries the optional sendMessage tuning read from config.
type telegramOptions struct {
	parseMode string // Bot API parse_mode; empty means plain text
	silent    bool   // disable_notification: deliver without sound
	threadID  int    // message_thread_id: target a forum topic
	hasThread bool   // whether a thread id was configured
}

// buildTelegram constructs a Telegram bot notifier from a config entry:
// `token` is the bot token (kept inside the API URL, never surfaced) and
// `chat_id` the numeric chat or `@channel` target. Optional `parse_mode`,
// `silent` and `message_thread_id` tune the sendMessage delivery.
func buildTelegram(name string, entry map[string]any) (Notifier, error) {
	token := cfgval.String(entry[KeyToken])
	if token == "" {
		return nil, errors.New("telegram notifier requires a token")
	}
	chatID := cfgval.String(entry[KeyChatID])
	if chatID == "" {
		return nil, errors.New("telegram notifier requires a chat_id")
	}
	opts := telegramOptions{
		parseMode: cfgval.String(entry[KeyParseMode]),
		silent:    cfgval.Bool(entry[KeySilent]),
	}
	if _, present := entry[KeyMessageThreadID]; present {
		if id, ok := cfgval.Int(entry[KeyMessageThreadID]); ok {
			opts.threadID, opts.hasThread = id, true
		}
	}
	return &webhookNotifier{
		name:    name,
		typ:     TypeTelegram,
		webhook: telegramAPIBase + token + telegramSendMessagePath,
		payload: func(msg Message) []byte { return telegramPayload(chatID, opts, msg) },
	}, nil
}

// telegramPayload renders the sendMessage body: the subject as the lead line
// and the detail (the SERMO_* fields) below it. Optional tuning fields are
// added only when configured, so an unconfigured notifier posts exactly the
// plain `chat_id`+`text` body it always did.
func telegramPayload(chatID string, opts telegramOptions, msg Message) []byte {
	text := msg.Subject
	if msg.Body != "" {
		text = msg.Subject + notifyLF + msg.Body
	}
	body := map[string]any{telegramChatIDKey: chatID, telegramTextKey: text}
	if opts.parseMode != "" {
		body[telegramParseModeKey] = opts.parseMode
	}
	if opts.silent {
		body[telegramSilentKey] = true
	}
	if opts.hasThread {
		body[telegramThreadIDKey] = opts.threadID
	}
	return webhookPayload(body)
}
