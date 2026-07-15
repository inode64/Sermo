package app

import (
	"context"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/web"
)

func (b *WebBackend) watchLiveView(ctx context.Context, w *webWatch, system metrics.Snapshot) (*web.WatchMeter, []web.WatchReading, string) {
	if w == nil {
		return nil, nil, ""
	}
	switch w.checkType {
	case checks.CheckTypeNet:
		return b.netWatchView(w)
	case checks.CheckTypeICMP:
		return b.icmpWatchView(w)
	case checks.CheckTypeSwap:
		return nil, nil, ""
	case checks.CheckTypeOOM:
		return b.oomWatchView()
	case checks.CheckTypeFDS:
		return b.fdsWatchView()
	case checks.CheckTypePIDs:
		return b.pidsWatchView()
	case checks.CheckTypePressure:
		return b.pressureWatchView(w)
	case checks.CheckTypeConntrack:
		return b.conntrackWatchView()
	case checks.CheckTypeEntropy:
		return b.entropyWatchView()
	case checks.CheckTypeZombies:
		return b.zombieWatchView()
	case checks.CheckTypeProcess:
		return b.processWatchView(w)
	case checks.CheckTypeAutofs:
		return b.autofsWatchView(w)
	case checks.CheckTypeDiskIO:
		return b.diskIOWatchView(w)
	case checks.CheckTypeSensors:
		return b.sensorsWatchView(w)
	case checks.CheckTypeRAID:
		return b.raidWatchView()
	case checks.CheckTypeEDAC:
		return b.edacWatchView()
	case checks.CheckTypeRoute:
		return b.routeWatchView(w)
	case checks.CheckTypeFile:
		return b.fileWatchView(ctx, w)
	case checks.CheckTypeCount:
		return b.countWatchView(ctx, w)
	case checks.CheckTypeFirewallRules:
		return b.firewallRulesWatchView(ctx, w)
	case checks.CheckTypeSize:
		return b.sizeWatchView(ctx, w)
	case checks.CheckTypeHdparm:
		return b.hdparmWatchView(ctx, w)
	case checks.CheckTypeSmart:
		return b.smartWatchView(ctx, w)
	default:
		if m := watchMeter(w.checkType, system); m != nil {
			return m, nil, ""
		}
		return b.probeWatchView(ctx, w)
	}
}

func watchErrorReadings(message string) []web.WatchReading {
	return []web.WatchReading{{Field: watchReadingFieldSample, Label: watchReadingLabelSample, Error: message}}
}

func watchPercent(value float64) string {
	return watchReadingMetricValue(value, watchReadingDefaultMetricDecimals, metrics.MetricUnitPercent)
}

func watchMetricEnabled(metricEntries map[string]any, metric string) bool {
	if len(metricEntries) == 0 {
		return true
	}
	_, ok := metricEntries[metric]
	return ok
}

func netErrorTotal(metricEntries map[string]any, counters map[string]uint64) uint64 {
	names := []string{checks.NetCounterRXErrors, checks.NetCounterTXErrors}
	if entry, ok := metricEntries[checks.NetMetricErrors].(map[string]any); ok {
		if configured := cfgval.StringArray(entry[checks.CheckKeyCounters]); len(configured) > 0 {
			names = configured
		}
	}
	var total uint64
	for _, name := range names {
		total += counters[name]
	}
	return total
}
