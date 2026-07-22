package config

import (
	"time"

	"sermo/internal/cfgval"
)

const (
	// DefaultEngineInterval is the fallback for engine.interval.
	DefaultEngineInterval = 30 * time.Second
	// DefaultEngineDiagnosticsInterval is the fallback for engine.diagnostics_interval.
	DefaultEngineDiagnosticsInterval = time.Hour
)

// EngineLogPath returns an engine log file path (access, events, diagnostics).
// An empty string means that log channel is disabled.
func EngineLogPath(cfg *Config, key string) string {
	if cfg == nil {
		return ""
	}
	engine, _ := cfg.Global.Raw[SectionEngine].(map[string]any)
	return cfgval.AsString(engine[key])
}

// EngineDiagnosticsInterval returns engine.diagnostics_interval, or fallback
// when unset/invalid.
func EngineDiagnosticsInterval(cfg *Config, fallback time.Duration) time.Duration {
	return EngineDuration(cfg, EngineKeyDiagnosticsInterval, fallback)
}

// EngineInterval returns engine.interval, or fallback when unset/invalid.
func EngineInterval(cfg *Config, fallback time.Duration) time.Duration {
	return EngineDuration(cfg, keyInterval, fallback)
}

// EngineDuration returns the engine.<key> duration, or fallback when the field
// is unset, unparseable or not positive.
func EngineDuration(cfg *Config, key string, fallback time.Duration) time.Duration {
	if cfg == nil {
		return fallback
	}
	engine, _ := cfg.Global.Raw[SectionEngine].(map[string]any)
	if d := cfgval.Duration(engine[key]); d > 0 {
		return d
	}
	return fallback
}
