package app

import (
	"context"
	"fmt"
	"os"
	"sermo/internal/config"
	"sermo/internal/servicemgr"
	"sermo/internal/units"
	"sermo/internal/web"
	"strings"
	"time"
)

// DaemonInfo returns the daemon's effective configuration and host identity.
func (b *WebBackend) DaemonInfo(_ context.Context) web.DaemonInfo {
	info := web.DaemonInfo{}

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
		info.HostUptime = formatInterval(up.Round(time.Second))
	}

	if b.cfg != nil {
		g := b.cfg.Global
		info.ConfigPath = g.Path
		info.RuntimeDir = g.RuntimeDir()
		info.StateDir = g.StateDir()

		// Engine block (effective values with documented fallbacks)
		info.Interval = formatInterval(config.EngineInterval(b.cfg, config.DefaultEngineInterval))
		info.MaxParallelChecks = EngineInt(b.cfg, config.EngineKeyMaxParallelChecks, DefaultEngineMaxParallelChecks)
		info.MaxParallelOperations = EngineInt(b.cfg, config.EngineKeyMaxParallelOperations, DefaultEngineMaxParallelOperations)
		info.DefaultTimeout = formatInterval(EngineDuration(b.cfg, config.EngineKeyDefaultTimeout, DefaultEngineCheckTimeout))
		info.OperationTimeout = formatInterval(EngineDuration(b.cfg, config.EngineKeyOperationTimeout, DefaultEngineOperationTimeout))
		info.StartupDelay = formatInterval(EngineDuration(b.cfg, config.EngineKeyStartupDelay, 0))

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

// formatInterval renders a duration for display, dropping zero components so a
// whole-hour interval reads "1h" instead of Go's default "1h0m0s". It extends
// Go's units upward with day (d), week (w) and month (mo, taken as 30 days) so
// long intervals stay compact: 24h reads "1d", 7d "1w", 30d "1mo", and mixed
// values chain greatest-first ("1mo1w", "1d6h", "1h30m"). A zero (or negative)
// duration is shown as "0s" — the only case where a 0 component survives.
// Sub-second durations keep the standard library formatting (e.g. "1.5s").
func formatInterval(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d%time.Second != 0 {
		return d.String()
	}
	total := int64(d / time.Second)
	const (
		minute = units.SecondsPerMinute
		hour   = units.MinutesPerHour * minute
		day    = units.HoursPerDay * hour
		week   = units.DaysPerWeek * day
		month  = units.DaysPerMonthApprox * day // display approximation
	)
	durationUnits := []struct {
		secs   int64
		suffix string
	}{
		{month, "mo"},
		{week, "w"},
		{day, "d"},
		{hour, "h"},
		{minute, "m"},
		{1, "s"},
	}
	var b strings.Builder
	for _, u := range durationUnits {
		if total >= u.secs {
			fmt.Fprintf(&b, "%d%s", total/u.secs, u.suffix)
			total %= u.secs
		}
	}
	return b.String()
}
