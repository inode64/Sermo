package config

import (
	"time"

	"sermo/internal/cfgval"
)

// EngineLogPath returns an engine log file path (access, events, diagnostics).
// An empty string means that log channel is disabled.
func EngineLogPath(cfg *Config, key string) string {
	if cfg == nil {
		return ""
	}
	engine, _ := cfg.Global.Raw["engine"].(map[string]any)
	return cfgval.AsString(engine[key])
}

// EngineDiagnosticsInterval returns engine.diagnostics_interval, or fallback
// when unset/invalid.
func EngineDiagnosticsInterval(cfg *Config, fallback time.Duration) time.Duration {
	if cfg == nil {
		return fallback
	}
	engine, _ := cfg.Global.Raw["engine"].(map[string]any)
	if d := cfgval.Duration(engine["diagnostics_interval"]); d > 0 {
		return d
	}
	return fallback
}

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
