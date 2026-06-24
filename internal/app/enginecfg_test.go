package app

import (
	"testing"

	"sermo/internal/config"
)

func TestEngineByteSize(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"engine": map[string]any{"state_cache_size": "32M"},
	}}}
	if got := EngineByteSize(cfg, "state_cache_size", 64<<20); got != 32<<20 {
		t.Fatalf("EngineByteSize = %d, want %d", got, 32<<20)
	}

	// Unset and unparseable values fall back.
	if got := EngineByteSize(cfg, "missing", 64<<20); got != 64<<20 {
		t.Fatalf("EngineByteSize(missing) = %d, want fallback %d", got, 64<<20)
	}
	bad := &config.Config{Global: config.Global{Raw: map[string]any{
		"engine": map[string]any{"state_cache_size": "lots"}, // no unit suffix
	}}}
	if got := EngineByteSize(bad, "state_cache_size", 64<<20); got != 64<<20 {
		t.Fatalf("EngineByteSize(bad) = %d, want fallback %d", got, 64<<20)
	}
}
