package notify

import (
	"errors"

	"sermo/internal/cfgval"
	"sermo/internal/telegramapi"
)

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
		webhook: telegramapi.MethodURL(token, telegramapi.MethodSendMessage),
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
	body := map[string]any{telegramapi.FieldChatID: chatID, telegramapi.FieldText: text}
	if opts.parseMode != "" {
		body[telegramapi.FieldParseMode] = opts.parseMode
	}
	if opts.silent {
		body[telegramapi.FieldSilent] = true
	}
	if opts.hasThread {
		body[telegramapi.FieldThreadID] = opts.threadID
	}
	return webhookPayload(body)
}
