package telegrambot

import (
	"testing"
	"time"
)

func TestParseConfigDefaultsAndClamp(t *testing.T) {
	if got := ParseConfig(nil); got.Enabled || got.Token != "" || got.AllowedChats != nil {
		t.Fatalf("nil section should be a disabled empty config, got %+v", got)
	}

	cfg := ParseConfig(map[string]any{
		"token":         "123:abc",
		"allowed_chats": []any{123, -1001234567890},
	})
	if !cfg.Enabled {
		t.Fatalf("present section without enabled:false should be enabled")
	}
	if cfg.PollInterval != DefaultPollInterval {
		t.Fatalf("poll interval = %s, want default %s", cfg.PollInterval, DefaultPollInterval)
	}
	if len(cfg.AllowedChats) != 2 || cfg.AllowedChats[0] != 123 || cfg.AllowedChats[1] != -1001234567890 {
		t.Fatalf("allowed chats = %v", cfg.AllowedChats)
	}

	if got := ParseConfig(map[string]any{"poll_interval": "1ms"}).PollInterval; got != MinPollInterval {
		t.Fatalf("tiny interval should clamp to %s, got %s", MinPollInterval, got)
	}
	if got := ParseConfig(map[string]any{"poll_interval": "24h"}).PollInterval; got != MaxPollInterval {
		t.Fatalf("huge interval should clamp to %s, got %s", MaxPollInterval, got)
	}
	if got := ParseConfig(map[string]any{"poll_interval": "45s"}).PollInterval; got != 45*time.Second {
		t.Fatalf("in-range interval should pass through, got %s", got)
	}
}

func TestConfigActiveAndAllows(t *testing.T) {
	if (Config{Enabled: true}).active() {
		t.Fatalf("enabled without token must not be active")
	}
	if (Config{Token: "t"}).active() {
		t.Fatalf("token without enabled must not be active")
	}
	c := Config{Enabled: true, Token: "t", AllowedChats: []int64{42}}
	if !c.active() {
		t.Fatalf("enabled+token must be active")
	}
	if !c.allows(42) || c.allows(43) {
		t.Fatalf("allow-list check wrong for %+v", c)
	}
	if (Config{}).allows(0) {
		t.Fatalf("empty allow-list must reject everyone")
	}
}
