package config

import (
	"time"

	"sermo/internal/cfgval"
)

// EngineInterval returns engine.interval, or fallback when unset/invalid.
func EngineInterval(cfg *Config, fallback time.Duration) time.Duration {
	if cfg == nil {
		return fallback
	}
	engine, _ := cfg.Global.Raw["engine"].(map[string]any)
	if d := cfgval.Duration(engine["interval"]); d > 0 {
		return d
	}
	return fallback
}
