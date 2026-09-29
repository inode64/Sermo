package telegrambot

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"sermo/internal/ctxutil"
	"sermo/internal/telegramapi"
)

const (
	// pollErrorBackoff paces retries after a getUpdates error so a persistent
	// failure (bad token, outage) does not spin.
	pollErrorBackoff = 5 * time.Second
	// idlePollInterval is how long Run waits between checks while disabled or
	// tokenless, so a reload can enable it without a restart.
	idlePollInterval = 5 * time.Second
	// maxReplyParts caps how many messages one command reply is split into;
	// the last part is truncated beyond it.
	maxReplyParts = 8
)

// Bot is a read-only Telegram command bot driven by long polling. Construct it
// once and reconfigure it on SIGHUP reload via UpdateConfig.
type Bot struct {
	reporter Reporter
	log      *slog.Logger

	mu     sync.Mutex
	cfg    Config
	client *client

	// offset is touched only by the Run goroutine, so it needs no lock.
	offset int64
	// idle overrides idlePollInterval in tests; zero means the constant.
	idle time.Duration
}

// New builds a bot from the initial config. reporter supplies report data;
// logger may be nil.
func New(reporter Reporter, cfg Config, logger *slog.Logger) *Bot {
	if logger == nil {
		logger = slog.Default()
	}
	b := &Bot{reporter: reporter, log: logger}
	b.UpdateConfig(cfg)
	return b
}

// UpdateConfig swaps the configuration used from the next poll (config reload).
// It rebuilds the API client when the token or poll interval changes, and drops
// it when the token is cleared.
func (b *Bot) UpdateConfig(cfg Config) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	rebuild := b.client == nil || cfg.Token != b.cfg.Token || cfg.PollInterval != b.cfg.PollInterval
	b.cfg = cfg
	switch {
	case cfg.Token == "":
		b.client = nil
	case rebuild:
		b.client = newClient(cfg.Token, cfg.PollInterval+pollClientMargin)
	}
}

// snapshot returns the current config and client under the lock.
func (b *Bot) snapshot() (Config, *client) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cfg, b.client
}

// Run polls Telegram for commands until ctx is cancelled. Whenever it starts
// polling a token — at startup, after a reload changes the token, or when a
// disabled or tokenless bot becomes active — it first discards the backlog
// queued for that token, so old commands are never replayed.
func (b *Bot) Run(ctx context.Context) {
	if b == nil {
		return
	}
	// skippedToken is the token whose backlog was discarded; empty while idle,
	// so becoming active again discards what queued meanwhile.
	skippedToken := ""
	for {
		if ctx.Err() != nil {
			return
		}
		cfg, cl := b.snapshot()
		if !cfg.active() || cl == nil {
			// Disabled or tokenless (possibly after a reload): idle rather than
			// busy-loop, and re-check on the next tick.
			skippedToken = ""
			if !ctxutil.Sleep(ctx, b.idleInterval()) {
				return
			}
			continue
		}
		if cfg.Token != skippedToken {
			// Update ids are per bot: an offset carried over from another token
			// would make getUpdates skip (and confirm) every new command, or
			// replay the new bot's queue.
			b.offset = 0
			b.skipBacklog(ctx, cl)
			skippedToken = cfg.Token
		}
		updates, err := cl.getUpdates(ctx, b.offset, cfg.PollInterval)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.log.Warn("telegram getUpdates failed", "error", err)
			if !ctxutil.Sleep(ctx, pollErrorBackoff) {
				return
			}
			continue
		}
		for _, u := range updates {
			b.offset = u.UpdateID + 1
			b.handleUpdate(ctx, cfg, cl, u)
		}
	}
}

// idleInterval is how long Run waits between checks while inactive.
func (b *Bot) idleInterval() time.Duration {
	if b.idle > 0 {
		return b.idle
	}
	return idlePollInterval
}

// skipBacklog advances the offset past updates already queued for cl's token
// without acting on them. Best effort: on error the offset stays at zero and
// the main loop proceeds.
func (b *Bot) skipBacklog(ctx context.Context, cl *client) {
	updates, err := cl.getUpdates(ctx, 0, 0)
	if err != nil {
		return
	}
	for _, u := range updates {
		if next := u.UpdateID + 1; next > b.offset {
			b.offset = next
		}
	}
}

// handleUpdate authorizes and dispatches one update, then replies. A panic in a
// handler is recovered so one bad command cannot stop the poll loop.
func (b *Bot) handleUpdate(ctx context.Context, cfg Config, cl *client, u update) {
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("telegram command panic", "recover", r)
		}
	}()
	msg := u.Message
	if msg == nil || strings.TrimSpace(msg.Text) == "" {
		return
	}
	if !cfg.allows(msg.Chat.ID) {
		// Never reply to a chat that is not on the allow-list.
		b.log.Warn("telegram command from unauthorized chat ignored", "chat_id", msg.Chat.ID)
		return
	}
	reply, err := b.dispatch(ctx, msg.Text)
	if err != nil {
		reply = "Error: " + err.Error()
	}
	if reply == "" {
		return
	}
	// A long list (/services on a large host, /events 50) exceeds the API's
	// per-message limit and would be rejected whole; send it in order as
	// several messages, capped so one command cannot flood the chat.
	for _, part := range telegramapi.SplitText(reply, maxReplyParts) {
		if err := cl.sendMessage(ctx, msg.Chat.ID, msg.MessageThreadID, part); err != nil {
			b.log.Warn("telegram sendMessage failed", "error", err)
			return
		}
	}
}
