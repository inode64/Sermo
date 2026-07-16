package app

import (
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/web"
)

// watchDashboardView returns the latest result published by the daemon watch
// cycle. The web handler never samples watches itself.
func (b *WebBackend) watchDashboardView(w *webWatch, system metrics.Snapshot) (*web.WatchMeter, []web.WatchReading, string) {
	if w == nil {
		return nil, nil, ""
	}
	return b.watchSnapshotView(w, system)
}

func (b *WebBackend) watchSnapshotView(w *webWatch, system metrics.Snapshot) (*web.WatchMeter, []web.WatchReading, string) {
	snaps := b.watchSnapshots.Get(w.name, w.checkType)
	if len(snaps) == 0 {
		if m := watchMeter(w.checkType, system); m != nil {
			return m, nil, ""
		}
		return nil, nil, ""
	}
	var meter *web.WatchMeter
	var readings []web.WatchReading
	var summaries []string
	for _, snap := range snaps {
		if !b.watchSnapshotCurrent(w, snap) || !watchSnapshotMetricConfigured(w, snap) {
			continue
		}
		rs := watchSnapshotReadings(w.checkType, snap)
		readings = append(readings, rs...)
		if meter == nil {
			meter = watchMeterFromSnapshot(w.checkType, snap.Data)
		}
		if summary := watchSnapshotSummary(snap, rs); summary != "" {
			summaries = append(summaries, summary)
		}
	}
	if meter == nil {
		meter = watchMeter(w.checkType, system)
	}
	return meter, readings, strings.Join(summaries, readingSummarySeparator)
}

func (b *WebBackend) watchSnapshotCurrent(w *webWatch, snap CheckSnapshot) bool {
	if snap.At.IsZero() {
		return false
	}
	return b.webNow().Sub(snap.At) <= runtimePublishMaxAge(w.interval)
}

func watchSnapshotMetricConfigured(w *webWatch, snap CheckSnapshot) bool {
	metric := cfgval.String(snap.Data[checks.DataKeyMetric])
	if metric == "" || len(w.metrics) == 0 {
		return true
	}
	_, ok := w.metrics[metric]
	return ok
}

func watchSnapshotReadings(checkType string, snap CheckSnapshot) []web.WatchReading {
	readings := checkReadings(checkType, snap.Data)
	if len(readings) == 0 && snap.Message != "" {
		readings = []web.WatchReading{{Field: watchReadingFieldResult, Label: watchReadingLabelResult, Value: snap.Message}}
	}
	if !snap.healthy() && snap.Message != "" {
		readings = append([]web.WatchReading{{Field: watchReadingFieldError, Label: watchReadingLabelError, Error: snap.Message}}, readings...)
	}
	return readings
}

func watchSnapshotSummary(snap CheckSnapshot, readings []web.WatchReading) string {
	if snap.Message != "" {
		return snap.Message
	}
	for _, r := range readings {
		if r.Error != "" {
			return r.Error
		}
		if r.Value != "" {
			return r.Value
		}
	}
	return ""
}

func watchMeterFromSnapshot(checkType string, data map[string]any) *web.WatchMeter {
	switch checkType {
	case checks.CheckTypeMemory:
		total, totalOK := uintField(data[checks.DataKeyTotalBytes])
		available, availableOK := uintField(data[checks.DataKeyAvailableBytes])
		usedPct, pctOK := cfgval.Float(data[checks.DataKeyUsedPct])
		if !totalOK || !availableOK || !pctOK {
			return nil
		}
		available = min(available, total)
		return &web.WatchMeter{
			Kind:       metrics.MetricMemory,
			UsedPct:    usedPct,
			TotalBytes: total,
			UsedBytes:  total - available,
			FreeBytes:  available,
		}
	case checks.CheckTypeLoad:
		load, loadOK := cfgval.Float(data[metrics.MetricLoad1])
		numCPU, cpuOK := cfgval.Int(data[checks.DataKeyNumCPU])
		if !loadOK || !cpuOK || numCPU <= 0 {
			return nil
		}
		return &web.WatchMeter{Kind: checks.CheckTypeLoad, UsedPct: load / float64(numCPU) * metrics.PercentScale, Load: load, NumCPU: numCPU}
	case checks.CheckTypeFDS:
		return watchCountMeter(checks.CheckTypeFDS, data, checks.DataKeyAllocated)
	case checks.CheckTypePIDs:
		return watchCountMeter(checks.CheckTypePIDs, data, checks.DataKeyCount)
	case checks.CheckTypeConntrack:
		return watchCountMeter(checks.CheckTypeConntrack, data, checks.DataKeyCount)
	default:
		return nil
	}
}

func watchCountMeter(kind string, data map[string]any, countKey string) *web.WatchMeter {
	count, countOK := uintField(data[countKey])
	limit, limitOK := uintField(data[checks.DataKeyMax])
	usedPct, pctOK := cfgval.Float(data[checks.DataKeyUsedPct])
	if !countOK || !limitOK || !pctOK || limit == 0 {
		return nil
	}
	return &web.WatchMeter{Kind: kind, UsedPct: usedPct, Count: count, Max: limit}
}
