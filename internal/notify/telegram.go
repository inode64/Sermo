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
	opts := telegramOptions{
		parseMode: cfgval.String(entry[KeyParseMode]),
		silent:    cfgval.Bool(entry[KeySilent]),
	}
	if _, present := entry[KeyMessageThreadID]; present {
		if id, ok := cfgval.Int(entry[KeyMessageThreadID]); ok {
			opts.threadID, opts.hasThread = id, true
		}
	}
	n := &webhookNotifier{
		name:    name,
		typ:     TypeTelegram,
		webhook: telegramapi.MethodURL(token, telegramapi.MethodSendMessage),
		payload: func(msg Message) []byte { return telegramPayload(chatID, opts, msg) },
	}
	if opts.parseMode != "" {
		// Subjects start with "[sermo]" and bodies carry SERMO_* names and
		// command output: unescaped, any of them makes the Bot API reject the
		// whole message ("can't parse entities").
		n.escape = func(s string) string { return telegramapi.EscapeText(opts.parseMode, s) }
	}
	return n, nil
}

// telegramPayload renders the sendMessage body: the subject as the lead line
// and the detail (the SERMO_* fields) below it. Optional tuning fields are
// added only when configured, so an unconfigured notifier posts exactly the
// plain `chat_id`+`text` body it always did.
func telegramPayload(chatID string, opts telegramOptions, msg Message) []byte {
	text, parseMode := telegramText(msg, opts.parseMode), opts.parseMode
	if telegramapi.TextLength(text) > telegramapi.MaxTextLength {
		// The API rejects the whole message, losing exactly the alert with the
		// most output. Cutting markup could split an escape or leave an entity
		// open, so an oversized message goes out as its plain text, truncated.
		if msg.raw != nil {
			text = telegramText(*msg.raw, "")
		}
		text, parseMode = telegramapi.TruncateText(text), ""
	}
	body := map[string]any{telegramapi.FieldChatID: chatID, telegramapi.FieldText: text}
	if parseMode != "" {
		body[telegramapi.FieldParseMode] = parseMode
	}
	if opts.silent {
		body[telegramapi.FieldSilent] = true
	}
	if opts.hasThread {
		body[telegramapi.FieldThreadID] = opts.threadID
	}
	return webhookPayload(body)
}

// telegramText leads with a coloured mark — the message's severity, or green
// for a recovery — then the subject, the detail below it and the dashboard
// link last. The link is escaped for parseMode like every other value.
func telegramText(msg Message, parseMode string) string {
	text := toneMarks[msg.tone()] + notifySP + msg.Subject
	if msg.Body != "" {
		text += notifyLF + msg.Body
	}
	if msg.Link != "" {
		link := msg.Link
		if parseMode != "" {
			link = telegramapi.EscapeText(parseMode, link)
		}
		text += notifyLF + link
	}
	return text
}
