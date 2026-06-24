package app

import (
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/process"
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

// EngineByteSize reads a byte-size field (e.g. "64M", "1G") from the engine
// block, falling back when unset or unparseable.
func EngineByteSize(cfg *config.Config, key string, fallback int64) int64 {
	if v, ok := cfgval.ByteSize(engineValue(cfg, key)); ok {
		return int64(v)
	}
	return fallback
}

// EngineUserLookup builds the user/group resolver configured under engine.
func EngineUserLookup(cfg *config.Config, runner execx.Runner) *process.UserLookup {
	return process.NewUserLookup(process.UserLookupConfig{
		Mode:    EngineString(cfg, "user_lookup"),
		Timeout: EngineDuration(cfg, "user_lookup_timeout", process.DefaultUserLookupTimeout),
		Runner:  runner,
	})
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
