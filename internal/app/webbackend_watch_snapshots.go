package app

import (
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/web"
)

// readingSummarySeparator joins the parts of a watch's one-line reading summary.
const readingSummarySeparator = " · "

// watchSnapshotView returns the latest result published by the daemon watch
// cycle. The web handler never samples watches itself.
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
		snapMeter := watchMeterFromSnapshot(w.checkType, snap.Data)
		rs := watchSnapshotReadings(w.checkType, w.severityFor(cfgval.String(snap.Data[checks.DataKeyMetric])), snap, snapMeter != nil)
		readings = append(readings, rs...)
		if meter == nil {
			meter = snapMeter
		}
		if summary := watchSnapshotSummary(snap, rs); summary != "" {
			summaries = append(summaries, summary)
		}
	}
	if meter == nil {
		meter = watchMeter(w.checkType, system)
	}
	return meter, dedupeWatchReadings(readings), strings.Join(summaries, readingSummarySeparator)
}

// dedupeWatchReadings drops the repeats a multi-metric watch produces. Every
// metric of a net watch samples the same interface, so each of them carries the
// same context rows — the interface name, its MAC, its driver, its slot — and
// the dashboard would otherwise print that whole block once per metric.
//
// Only value rows collapse. An error or a warning row is a report about one
// metric, so two metrics failing at once are two distinct findings even though
// they share a field name, and collapsing them would hide one of them.
func dedupeWatchReadings(readings []web.WatchReading) []web.WatchReading {
	seen := make(map[string]bool, len(readings))
	out := make([]web.WatchReading, 0, len(readings))
	for _, r := range readings {
		if r.Error != "" || r.Warning != "" {
			out = append(out, r)
			continue
		}
		if seen[r.Field] {
			continue
		}
		seen[r.Field] = true
		out = append(out, r)
	}
	return out
}

func (b *WebBackend) watchSnapshotCurrent(w *webWatch, snap CheckSnapshot) bool {
	// Name, type and age are not enough: a reload that keeps the watch name
	// while pointing it at another device must not show the previous target.
	return w != nil && snapshotConfigMatches(w.configID, snap.ConfigID) && b.watchSampleCurrent(w, snap.At)
}

func (b *WebBackend) watchSampleCurrent(w *webWatch, at time.Time) bool {
	if w == nil || at.IsZero() {
		return false
	}
	return b.webNow().Sub(at) <= runtimePublishMaxAge(w.interval)
}

// watchSampleState classifies the newest daemon-published result without
// exposing stale data or asking the web handler to run the watch itself.
func (b *WebBackend) watchSampleState(w *webWatch, checkedAt time.Time) string {
	if b.watchSnapshots == nil || w == nil {
		return ""
	}
	if checkedAt.IsZero() {
		return web.WatchSampleStateCollecting
	}
	if b.watchSampleCurrent(w, checkedAt) {
		return web.WatchSampleStateFresh
	}
	return web.WatchSampleStateStale
}

func watchSnapshotMetricConfigured(w *webWatch, snap CheckSnapshot) bool {
	metric := cfgval.String(snap.Data[checks.DataKeyMetric])
	if metric == "" || len(w.metrics) == 0 {
		return true
	}
	_, ok := w.metrics[metric]
	return ok
}

// severityFor resolves how grave one published sample is, narrowing this watch's
// own gravity by the metric block that produced it. A net watch can therefore
// call its error counter an advisory while its link state stays an outage.
func (w *webWatch) severityFor(metric string) string {
	if w == nil {
		return checks.SeverityError
	}
	declared := ""
	if m, ok := w.metrics[metric].(map[string]any); ok {
		declared = cfgval.AsString(m[checks.CheckKeySeverity])
	}
	return checks.ResolveSeverity(declared, w.severity)
}

// watchSnapshotReadings turns one snapshot into the rows its expansion shows.
//
// gauged says a meter will be drawn from this same snapshot. When one is, the
// result line is not repeated below it: the gauge already states the count, the
// ceiling and the utilisation, and the row above the expansion carries the same
// sentence as its summary. An expansion earns its space by adding to the row, not
// by restating it. A failure is never suppressed — that is not a repetition of
// the gauge, it is the thing the gauge cannot say.
func watchSnapshotReadings(checkType, severity string, snap CheckSnapshot, gauged bool) []web.WatchReading {
	readings := checkReadings(checkType, snap.Data)
	if len(readings) == 0 && snap.Message != "" && !gauged {
		readings = []web.WatchReading{{Field: watchReadingFieldResult, Label: watchReadingLabelResult, Value: snap.Message}}
	}
	if !snap.healthy() && snap.Message != "" {
		// An advisory reports through Warning, never Error: a non-empty Error is
		// precisely what paints the row red.
		bad := web.WatchReading{Field: watchReadingFieldError, Label: watchReadingLabelError, Error: snap.Message}
		if checks.IsWarning(severity) {
			bad = web.WatchReading{Field: watchReadingFieldWarning, Label: watchReadingLabelWarning, Warning: snap.Message}
		}
		readings = append([]web.WatchReading{bad}, readings...)
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
		if r.Warning != "" {
			return r.Warning
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
		return memoryWatchMeter(total, available, usedPct)
	case checks.CheckTypeLoad:
		load, loadOK := cfgval.Float(data[metrics.MetricLoad1])
		numCPU, cpuOK := cfgval.Int(data[checks.DataKeyNumCPU])
		if !loadOK || !cpuOK {
			return nil
		}
		return loadWatchMeter(load, numCPU)
	default:
		countKey, ok := checks.MeterCountKey(checkType)
		if !ok {
			return nil
		}
		return watchCountMeter(checkType, data, countKey)
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
