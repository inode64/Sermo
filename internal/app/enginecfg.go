package app

import (
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/config"
)

func engineMap(cfg *config.Config) map[string]any {
	if cfg == nil {
		return nil
	}
	m, _ := cfg.Global.Raw["engine"].(map[string]any)
	return m
}

func engineValue(cfg *config.Config, key string) any {
	return engineMap(cfg)[key]
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
	return cfgval.AsString(engineValue(cfg, key))
}

func engineDuration(cfg *config.Config, key string, fallback time.Duration) time.Duration {
	if d := cfgval.Duration(engineValue(cfg, key)); d > 0 {
		return d
	}
	return fallback
}

func engineInt(cfg *config.Config, key string, fallback int) int {
	if v, ok := cfgval.Int(engineValue(cfg, key)); ok {
		return v
	}
	return fallback
}
