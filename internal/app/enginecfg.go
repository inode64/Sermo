package app

import (
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/state"
)

const (
	// DefaultEngineArtifactInterval is the fallback cadence for catalog apps,
	// libraries and service config/version artifact monitors.
	DefaultEngineArtifactInterval = 5 * time.Minute
	// DefaultEngineCheckTimeout is the fallback for engine.default_timeout.
	DefaultEngineCheckTimeout = 10 * time.Second
	// DefaultEngineOperationTimeout is the fallback for engine.operation_timeout.
	DefaultEngineOperationTimeout = operation.DefaultOperationTimeout
	// DefaultEngineMaxParallelChecks is the fallback check worker limit.
	DefaultEngineMaxParallelChecks = 8
)

func engineMap(cfg *config.Config) map[string]any {
	if cfg == nil {
		return nil
	}
	m, _ := cfg.Global.Raw[config.SectionEngine].(map[string]any)
	return m
}

func engineValue(cfg *config.Config, key string) any {
	return engineMap(cfg)[key]
}

// EngineDuration reads a duration field from the engine block.
func EngineDuration(cfg *config.Config, key string, fallback time.Duration) time.Duration {
	return config.EngineDuration(cfg, key, fallback)
}

// EngineInt reads an int field from the engine block.
func EngineInt(cfg *config.Config, key string, fallback int) int {
	return engineInt(cfg, key, fallback)
}

// EngineBoolDefaultTrue reads a boolean field from the engine block that is on
// unless the operator explicitly turns it off. An absent or non-boolean value
// keeps the feature enabled; validation is what rejects the non-boolean.
func EngineBoolDefaultTrue(cfg *config.Config, key string) bool {
	enabled, ok := engineValue(cfg, key).(bool)
	return !ok || enabled
}

// EngineString reads a string field from the engine block ("" when unset).
func EngineString(cfg *config.Config, key string) string {
	return cfgval.AsString(engineValue(cfg, key))
}

// EngineByteSize reads a byte-size field (e.g. "64M", "1G") from the engine
// block, falling back when unset or unparseable.
func EngineByteSize(cfg *config.Config, key string, fallback int64) int64 {
	if v, ok := cfgval.ByteSize(engineValue(cfg, key)); ok {
		return uintToInt64(v)
	}
	return fallback
}

// EngineRetention reads the stored-history window of every archive resolution
// from the engine block. A key left unset keeps that archive's default.
func EngineRetention(cfg *config.Config) state.Retention {
	defaults := state.DefaultRetention()
	return state.Retention{
		Minute:      EngineDuration(cfg, config.EngineKeyRetention1m, defaults.Minute),
		FiveMinutes: EngineDuration(cfg, config.EngineKeyRetention5m, defaults.FiveMinutes),
		Hour:        EngineDuration(cfg, config.EngineKeyRetention1h, defaults.Hour),
		SixHours:    EngineDuration(cfg, config.EngineKeyRetention6h, defaults.SixHours),
		Day:         EngineDuration(cfg, config.EngineKeyRetention1d, defaults.Day),
		Events:      EngineDuration(cfg, config.EngineKeyRetentionEvents, defaults.Events),
	}
}

// EngineRollupInterval reads how often the daemon consolidates and prunes stored
// history. It must stay well below engine.retention_1m: the per-minute archive is
// the source every coarser one reads, and its prune is floored at the
// consolidation watermark, so a slower cadence delays reclaiming space.
func EngineRollupInterval(cfg *config.Config) time.Duration {
	return EngineDuration(cfg, config.EngineKeyRollupInterval, state.DefaultRollupInterval)
}

// EngineStateOptions builds the state store options from the engine block, so
// sermod and sermoctl open the same database with the same cache and retention.
func EngineStateOptions(cfg *config.Config) state.Options {
	return state.Options{
		CacheBytes: EngineByteSize(cfg, config.EngineKeyStateCacheSize, state.DefaultCacheBytes),
		Retention:  EngineRetention(cfg),
	}
}

// EngineUserLookup builds the user/group resolver configured under engine.
func EngineUserLookup(cfg *config.Config, runner execx.Runner) *process.UserLookup {
	return process.NewUserLookup(process.UserLookupConfig{
		Mode:    EngineString(cfg, config.EngineKeyUserLookup),
		Timeout: EngineDuration(cfg, config.EngineKeyUserLookupTimeout, process.DefaultUserLookupTimeout),
		Runner:  runner,
	})
}

func engineInt(cfg *config.Config, key string, fallback int) int {
	if v, ok := cfgval.Int(engineValue(cfg, key)); ok {
		return v
	}
	return fallback
}
