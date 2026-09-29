// Package telegramapi names the Telegram Bot API surface Sermo speaks: the
// endpoint, the methods and the request field names.
//
// Two packages talk to the same API for different reasons — internal/notify
// pushes alerts through sendMessage, internal/telegrambot long-polls getUpdates
// and replies — and each had grown its own copy of the base URL and field
// names. Keeping the wire vocabulary here means a change to the protocol lands
// in one place instead of silently diverging between the two.
//
// This is the protocol surface only. The matching `chat_id` / `parse_mode`
// configuration keys live in internal/notify: they coincide with these strings
// because the configuration mirrors the API, not because they are the same
// thing, and tying them together would make a config rename an API change.
package telegramapi

import (
	"slices"
	"strings"
)

// APIBase is the Bot API endpoint prefix. A bot token is appended directly to
// it, so it is also the reason every error raised around these calls has to be
// scrubbed of its URL before it can be logged.
const APIBase = "https://api.telegram.org/bot"

// Bot API methods Sermo calls.
const (
	MethodSendMessage = "sendMessage"
	MethodGetUpdates  = "getUpdates"
)

// sendMessage request fields.
const (
	FieldChatID    = "chat_id"
	FieldText      = "text"
	FieldParseMode = "parse_mode"
	// FieldSilent delivers the message without a notification sound.
	FieldSilent = "disable_notification"
	// FieldThreadID targets a forum topic; absent means the main timeline.
	FieldThreadID = "message_thread_id"
)

// getUpdates request fields.
const (
	FieldOffset         = "offset"
	FieldTimeout        = "timeout"
	FieldAllowedUpdates = "allowed_updates"
)

// UpdateTypeMessage is the only update kind the bot subscribes to.
const UpdateTypeMessage = "message"

// `parse_mode` values the API accepts.
const (
	ParseModeHTML       = "HTML"
	ParseModeMarkdown   = "Markdown"
	ParseModeMarkdownV2 = "MarkdownV2"
)

// parseModes are the `parse_mode` values the API accepts, sorted.
var parseModes = []string{ParseModeHTML, ParseModeMarkdown, ParseModeMarkdownV2}

// Per-mode escapers for literal text. The character sets are the ones the Bot
// API "Formatting options" section lists as reserved outside an entity: any
// unescaped occurrence makes sendMessage fail with "can't parse entities".
var (
	markdownV2Escaper = newBackslashEscaper("\\_*[]()~`>#+-=|{}.!")
	markdownEscaper   = newBackslashEscaper("_*`[")
	htmlEscaper       = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
)

// replacerPairSize is the old/new argument count per strings.Replacer rule.
const replacerPairSize = 2

func newBackslashEscaper(reserved string) *strings.Replacer {
	pairs := make([]string, 0, replacerPairSize*len(reserved))
	for _, r := range reserved {
		pairs = append(pairs, string(r), `\`+string(r))
	}
	return strings.NewReplacer(pairs...)
}

// EscapeText makes s render literally under parseMode. An empty or unknown
// mode is plain text and s is returned unchanged.
func EscapeText(parseMode, s string) string {
	switch parseMode {
	case ParseModeMarkdownV2:
		return markdownV2Escaper.Replace(s)
	case ParseModeMarkdown:
		return markdownEscaper.Replace(s)
	case ParseModeHTML:
		return htmlEscaper.Replace(s)
	default:
		return s
	}
}

// ParseModes returns the accepted `parse_mode` values, for validation and docs.
func ParseModes() []string { return slices.Clone(parseModes) }

// ValidParseMode reports whether s is an accepted `parse_mode`.
func ValidParseMode(s string) bool { return slices.Contains(parseModes, s) }

// MethodURL renders the endpoint for one method call with the bot token
// embedded, the single spelling both callers share.
func MethodURL(token, method string) string { return MethodURLAt(APIBase, token, method) }

// MethodURLAt renders a method endpoint at an explicitly supplied API base.
func MethodURLAt(base, token, method string) string { return base + token + "/" + method }
