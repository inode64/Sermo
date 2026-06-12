package app

import (
	"time"

	"sermo/internal/config"
)

func engineMap(cfg *config.Config) map[string]any {
	if cfg == nil {
		return nil
	}
	m, _ := cfg.Global.Raw["engine"].(map[string]any)
	return m
}

// EngineInterval returns engine.interval, or fallback when unset/invalid.
func EngineInterval(cfg *config.Config, fallback time.Duration) time.Duration {
	return engineDuration(cfg, "interval", fallback)
}

// EngineDuration reads a duration field from the engine block.
func EngineDuration(cfg *config.Config, key string, fallback time.Duration) time.Duration {
	return engineDuration(cfg, key, fallback)
}

// EngineInt reads an int field from the engine block.
func EngineInt(cfg *config.Config, key string, fallback int) int {
	return engineInt(cfg, key, fallback)
}

// EngineString reads a string field from the engine block ("" when unset).
func EngineString(cfg *config.Config, key string) string {
	s, _ := engineMap(cfg)[key].(string)
	return s
}

func engineDuration(cfg *config.Config, key string, fallback time.Duration) time.Duration {
	s, _ := engineMap(cfg)[key].(string)
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func engineInt(cfg *config.Config, key string, fallback int) int {
	switch v := engineMap(cfg)[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case uint64:
		return int(v)
	case float64:
		return int(v)
	default:
		return fallback
	}
}

// NotifiersRaw returns the global `notifiers` section (nil when absent), the
// input notify.Build expects.
func NotifiersRaw(cfg *config.Config) map[string]any {
	if cfg == nil {
		return nil
	}
	m, _ := cfg.Global.Raw["notifiers"].(map[string]any)
	return m
}
