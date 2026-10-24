package app

import (
	"context"
	"path/filepath"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/emission"
	"sermo/internal/execx"
	"sermo/internal/operation"
	"sermo/internal/process"
	"sermo/internal/rules"
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

// OpenStateStore opens the persistent store with the engine settings shared by
// sermod and sermoctl.
func OpenStateStore(ctx context.Context, cfg *config.Config) (*state.Store, error) {
	//nolint:wrapcheck // sermod and sermoctl add their own operation-specific context.
	return state.OpenContextWith(ctx, filepath.Join(cfg.Global.StateDir(), state.Filename), EngineStateOptions(cfg))
}

// EngineUserLookup builds the user/group resolver configured under engine.
func EngineUserLookup(cfg *config.Config, runner execx.Runner) *process.UserLookup {
	return process.NewUserLookup(process.UserLookupConfig{
		Mode:    config.EngineString(cfg, config.EngineKeyUserLookup),
		Timeout: config.EngineDuration(cfg, config.EngineKeyUserLookupTimeout, process.DefaultUserLookupTimeout),
		Runner:  runner,
	})
}

// ApplyEngineConfig derives from cfg the engine settings and the shared /proc
// readers the daemon runs on. sermod's first generation and every reload apply
// it, so both observe processes, users and limits the same way. wrap, when set,
// wraps each OS reader before it is cached (a test seam); the check limiter is
// created once and resized afterwards, so builders keep sharing it.
func ApplyEngineConfig(deps *Deps, cfg *config.Config, wrap func(process.OSReader) process.Reader) {
	deps.Runtime = cfg.Global.RuntimeDir()
	deps.Interval = config.EngineInterval(cfg)
	deps.DefaultTimeout = config.EngineDuration(cfg, config.EngineKeyDefaultTimeout, DefaultEngineCheckTimeout)
	deps.OperationTimeout = config.EngineDuration(cfg, config.EngineKeyOperationTimeout, DefaultEngineOperationTimeout)
	deps.MaxParallel = config.EngineInt(cfg, config.EngineKeyMaxParallelChecks, DefaultEngineMaxParallelChecks)
	if deps.CheckLimiter == nil {
		deps.CheckLimiter = checks.NewLimiter(deps.MaxParallel)
	} else {
		deps.CheckLimiter.SetLimit(deps.MaxParallel)
	}
	deps.UserLookup = EngineUserLookup(cfg, deps.ExecxRunner)
	deps.SystemFreshness = deps.Interval / SystemFreshnessIntervalDivisor
	cached := func(os process.OSReader) *process.CachingReader {
		var inner process.Reader = os
		if wrap != nil {
			inner = wrap(os)
		}
		return process.NewCachingReader(inner, deps.SystemFreshness)
	}
	// One shared /proc snapshot for discovery: concurrent workers and web
	// queries within a cycle reuse a single walk instead of each scanning every
	// PID. A reload rebuilds it so lookup policy and freshness follow config.
	procReader := cached(process.OSReader{LookupUserName: deps.UserLookup.Username})
	deps.ProcReader = procReader
	deps.ZombieSampler = checks.NewZombieSampler(procReader)
	terminalReader := cached(process.OSReader{
		LookupUserName:  deps.UserLookup.Username,
		LookupGroupName: deps.UserLookup.GroupName,
		ReadTTY:         true,
	})
	deps.SSHIdleSampler = checks.NewSSHIdleSampler(terminalReader, deps.UserLookup)
	deps.SSHSessionSampler = checks.NewSSHSessionSampler(terminalReader, deps.UserLookup)
	deps.TerminalProcessReader = terminalReader
	deps.GlobalNotify = config.NotifyDefault(cfg.Global.Raw)
	deps.GlobalEmission = emission.Merge(cfg.Global.Raw[emission.Section], emission.Default())
	deps.GlobalClear = rules.ClearWindowOrDefault(cfg.Global.Defaults()[rules.SectionClearWindow])
}

// withCheckLimiter supplies a budget for standalone builders. The daemon passes
// an existing limiter so all builders and configuration generations share it.
func (d Deps) withCheckLimiter() Deps {
	if d.CheckLimiter == nil && d.MaxParallel > 0 {
		d.CheckLimiter = checks.NewLimiter(d.MaxParallel)
	}
	return d
}
