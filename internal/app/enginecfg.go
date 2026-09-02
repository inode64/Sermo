package app

import (
	"time"

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

// EngineRetention reads the stored-history window of every archive resolution
// from the engine block. A key left unset keeps that archive's default.
func EngineRetention(cfg *config.Config) state.Retention {
	defaults := state.DefaultRetention()
	return state.Retention{
		Minute:      config.EngineDuration(cfg, config.EngineKeyRetention1m, defaults.Minute),
		FiveMinutes: config.EngineDuration(cfg, config.EngineKeyRetention5m, defaults.FiveMinutes),
		Hour:        config.EngineDuration(cfg, config.EngineKeyRetention1h, defaults.Hour),
		SixHours:    config.EngineDuration(cfg, config.EngineKeyRetention6h, defaults.SixHours),
		Day:         config.EngineDuration(cfg, config.EngineKeyRetention1d, defaults.Day),
		Events:      config.EngineDuration(cfg, config.EngineKeyRetentionEvents, defaults.Events),
	}
}

// EngineRollupInterval reads how often the daemon consolidates and prunes stored
// history. It must stay well below engine.retention_1m: the per-minute archive is
// the source every coarser one reads, and its prune is floored at the
// consolidation watermark, so a slower cadence delays reclaiming space.
func EngineRollupInterval(cfg *config.Config) time.Duration {
	return config.EngineDuration(cfg, config.EngineKeyRollupInterval, state.DefaultRollupInterval)
}

// EngineStateOptions builds the state store options from the engine block, so
// sermod and sermoctl open the same database with the same cache and retention.
func EngineStateOptions(cfg *config.Config) state.Options {
	return state.Options{
		CacheBytes: config.EngineByteSize(cfg, config.EngineKeyStateCacheSize, state.DefaultCacheBytes),
		Retention:  EngineRetention(cfg),
	}
}

// EngineUserLookup builds the user/group resolver configured under engine.
func EngineUserLookup(cfg *config.Config, runner execx.Runner) *process.UserLookup {
	return process.NewUserLookup(process.UserLookupConfig{
		Mode:    config.EngineString(cfg, config.EngineKeyUserLookup),
		Timeout: config.EngineDuration(cfg, config.EngineKeyUserLookupTimeout, process.DefaultUserLookupTimeout),
		Runner:  runner,
	})
}
