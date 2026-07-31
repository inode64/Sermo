// Package telegrambot implements an interactive, read-only Telegram bot that
// answers operator commands (/status, /services, /sla, ...) with Sermo reports.
//
// It receives commands over Bot API long polling (getUpdates), so it needs no
// inbound socket and no public exposure — matching Sermo's outbound-only
// posture. It is configured under the top-level `telegram_bot` section and,
// like the notifier transports, only ever reads state: it can never change the
// host. Report data is supplied through the narrow Reporter interface so this
// package builds and tests free of the daemon's platform-specific
// dependencies; the daemon wires an adapter over its web backend and store.
package telegrambot

import (
	"slices"
	"time"

	"sermo/internal/cfgval"
)

// Config field keys inside the `telegram_bot` section.
const (
	KeyEnabled      = "enabled"
	KeyToken        = "token"
	KeyAllowedChats = "allowed_chats"
	KeyPollInterval = "poll_interval"
)

const (
	// DefaultPollInterval is the getUpdates long-poll timeout when unset.
	DefaultPollInterval = 30 * time.Second
	// MinPollInterval is the lowest permitted configured poll interval.
	MinPollInterval = 1 * time.Second
	// MaxPollInterval is the highest permitted configured poll interval.
	MaxPollInterval = 10 * time.Minute
)

// Config is the parsed `telegram_bot` section.
type Config struct {
	Enabled      bool
	Token        string
	AllowedChats []int64
	PollInterval time.Duration
}

// ParseConfig reads a `telegram_bot` section map into a Config. A nil/absent
// section yields a disabled Config. Parsing is lenient: schema problems are
// reported by config validation, not here.
func ParseConfig(raw map[string]any) Config {
	if raw == nil {
		return Config{}
	}
	cfg := Config{
		Enabled:      !cfgval.Disabled(raw),
		Token:        cfgval.String(raw[KeyToken]),
		PollInterval: clampInterval(cfgval.DurationOr(raw[KeyPollInterval], DefaultPollInterval)),
	}
	if ids, ok := cfgval.IntList(raw[KeyAllowedChats]); ok {
		cfg.AllowedChats = make([]int64, len(ids))
		for i, id := range ids {
			cfg.AllowedChats[i] = int64(id)
		}
	}
	return cfg
}

func clampInterval(d time.Duration) time.Duration {
	switch {
	case d < MinPollInterval:
		return MinPollInterval
	case d > MaxPollInterval:
		return MaxPollInterval
	default:
		return d
	}
}

// active reports whether the bot should poll: enabled and holding a token.
func (c Config) active() bool { return c.Enabled && c.Token != "" }

// allows reports whether chat id may command the bot.
func (c Config) allows(chatID int64) bool {
	return slices.Contains(c.AllowedChats, chatID)
}
