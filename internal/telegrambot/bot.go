package telegrambot

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	// pollErrorBackoff paces retries after a getUpdates error so a persistent
	// failure (bad token, outage) does not spin.
	pollErrorBackoff = 5 * time.Second
	// idlePollInterval is how long Run waits between checks while disabled or
	// tokenless, so a reload can enable it without a restart.
	idlePollInterval = 5 * time.Second
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

// Run polls Telegram for commands until ctx is cancelled. It first discards any
// backlog queued before startup so a restart does not replay old commands.
func (b *Bot) Run(ctx context.Context) {
	if b == nil {
		return
	}
	b.skipBacklog(ctx)
	for {
		if ctx.Err() != nil {
			return
		}
		cfg, cl := b.snapshot()
		if !cfg.active() || cl == nil {
			// Disabled or tokenless (possibly after a reload): idle rather than
			// busy-loop, and re-check on the next tick.
			if !sleepCtx(ctx, idlePollInterval) {
				return
			}
			continue
		}
		updates, err := cl.getUpdates(ctx, b.offset, cfg.PollInterval)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.log.Warn("telegram getUpdates failed", "error", err)
			if !sleepCtx(ctx, pollErrorBackoff) {
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

// skipBacklog advances the offset past updates already queued at startup
// without acting on them. Best effort: on error the offset stays at zero and
// the main loop proceeds.
func (b *Bot) skipBacklog(ctx context.Context) {
	cfg, cl := b.snapshot()
	if !cfg.active() || cl == nil {
		return
	}
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
	if err := cl.sendMessage(ctx, msg.Chat.ID, msg.MessageThreadID, reply); err != nil {
		b.log.Warn("telegram sendMessage failed", "error", err)
	}
}

// sleepCtx waits d or returns false if ctx is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
