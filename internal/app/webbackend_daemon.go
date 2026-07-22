package app

import (
	"context"
	"os"
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/servicemgr"
	"sermo/internal/units"
	"sermo/internal/web"
	"time"
)

// DaemonInfo returns the daemon's effective configuration and host identity.
func (b *WebBackend) DaemonInfo(_ context.Context) web.DaemonInfo {
	info := web.DaemonInfo{}
	info.ActiveUsers = notify.ActiveUserCount()

	if h, err := os.Hostname(); err == nil {
		info.Hostname = h
	}
	info.OS = osPrettyName()
	if b.hostType != nil {
		info.HostType = b.hostType
	} else {
		info.HostType = hostTypeInfo()
	}
	if up, ok := hostUptime(); ok {
		info.HostUptimeSeconds = int64(up.Seconds())
		info.HostUptime = units.HumanizeDuration(up.Round(time.Second))
	}

	if b.cfg != nil {
		g := b.cfg.Global
		info.ConfigPath = g.Path
		info.RuntimeDir = g.RuntimeDir()
		info.StateDir = g.StateDir()

		// Engine block (effective values with documented fallbacks)
		info.Interval = units.HumanizeDuration(config.EngineInterval(b.cfg, config.DefaultEngineInterval))
		info.MaxParallelChecks = EngineInt(b.cfg, config.EngineKeyMaxParallelChecks, DefaultEngineMaxParallelChecks)
		info.DefaultTimeout = units.HumanizeDuration(EngineDuration(b.cfg, config.EngineKeyDefaultTimeout, DefaultEngineCheckTimeout))
		info.OperationTimeout = units.HumanizeDuration(EngineDuration(b.cfg, config.EngineKeyOperationTimeout, DefaultEngineOperationTimeout))
		info.StartupDelay = units.HumanizeDuration(EngineDuration(b.cfg, config.EngineKeyStartupDelay, 0))

		if em := engineMap(b.cfg); em != nil {
			if be, ok := em[config.EngineKeyBackend].(string); ok && be != "" {
				info.Backend = be
			}
		}
		if info.Backend == "" {
			info.Backend = string(servicemgr.BackendAuto)
		}
	}

	return info
}

// DaemonMetrics returns current and historical resource usage for the running
// sermod process.
func (b *WebBackend) DaemonMetrics(_ context.Context, since time.Duration) web.DaemonMetrics {
	if b.daemonMetrics == nil {
		return web.DaemonMetrics{Since: since.String()}
	}
	return b.daemonMetrics.Series(since)
}
