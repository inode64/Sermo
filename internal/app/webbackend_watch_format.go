package app

import (
	"fmt"
	"runtime"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/web"
	"strconv"
	"strings"
)

const (
	watchReadingFieldEntries = "entries"
	watchReadingNumericBase  = 10
)

// pluralSuffix returns the suffix to append to singular to form its plural for
// count items: "" when count is 1, "es" for sibilant endings (process ->
// processes, address -> addresses) and "s" otherwise (mountpoint -> mountpoints).
func pluralSuffix(count int, singular string) string {
	if count == 1 {
		return ""
	}
	switch {
	case strings.HasSuffix(singular, "s"), strings.HasSuffix(singular, "x"),
		strings.HasSuffix(singular, "z"), strings.HasSuffix(singular, "ch"),
		strings.HasSuffix(singular, "sh"):
		return "es"
	default:
		return "s"
	}
}

func processPIDList(samples []ProcInfo) string {
	return limitedDisplayList(samples, func(sample ProcInfo) string { return strconv.Itoa(sample.PID) })
}

// limitedDisplayList keeps snapshot details bounded while making the omitted
// count explicit. Process watches and process policies use the same limit so a
// busy host cannot inflate an event, API response or dashboard row.
func limitedDisplayList[T any](items []T, format func(T) string) string {
	parts := make([]string, 0, min(len(items), processPIDListLimit)+1)
	for i, item := range items {
		if i >= processPIDListLimit {
			break
		}
		parts = append(parts, format(item))
	}
	if extra := len(items) - processPIDListLimit; extra > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", extra))
	}
	return strings.Join(parts, displayListSeparator)
}

// swapWatchInfo reads the host swap usage from the collector's cached system
// snapshot (shared with the overview tiles, no extra probe). nil when the host
// has no swap or no collector is wired.
func swapWatchInfo(system metrics.Snapshot) *web.SwapWatchInfo {
	r := system[metrics.MetricTotalSwap]
	used, total, free, ok := byteUsage(r)
	if !ok {
		return nil
	}
	return &web.SwapWatchInfo{
		TotalBytes: total,
		UsedBytes:  used,
		FreeBytes:  free,
		UsedPct:    r.Percent,
	}
}

// byteUsage reads a capacity-carrying usage Reading (memory/swap) as used/total/
// free bytes, clamping free so a "used" momentarily above total cannot underflow
// the unsigned subtraction. ok is false when the reading carries no capacity
// (no total), including the zero Reading a missing metric yields.
func byteUsage(r metrics.Reading) (used, total, free uint64, ok bool) {
	if !r.HasTotal || r.Total <= 0 {
		return 0, 0, 0, false
	}
	used, total = uint64(r.Absolute), uint64(r.Total)
	return used, total, total - min(used, total), true
}

// watchMeter builds the generic usage gauge (progress bar) for host watch types
// served by the collector's cached system snapshot (shared with overview tiles,
// no extra probe). nil for any other type, or when the needed data is unavailable.
func watchMeter(checkType string, system metrics.Snapshot) *web.WatchMeter {
	switch checkType {
	case metrics.MetricMemory:
		r := system[metrics.MetricTotalMemory]
		_, total, available, ok := byteUsage(r)
		if !ok {
			return nil
		}
		return memoryWatchMeter(total, available, r.Percent)
	case checks.CheckTypeLoad:
		r, ok := system[metrics.MetricLoad1]
		if !ok || !r.HasAbsolute {
			return nil
		}
		return loadWatchMeter(r.Absolute, runtime.NumCPU())
	}
	return nil
}

// memoryWatchMeter is the canonical projection of one memory observation onto
// the web gauge. Callers retain ownership of translating their source into
// total and available bytes; this helper normalizes the presentation contract.
func memoryWatchMeter(total, available uint64, usedPct float64) *web.WatchMeter {
	available = min(available, total)
	return &web.WatchMeter{
		Kind:       metrics.MetricMemory,
		UsedPct:    usedPct,
		TotalBytes: total,
		UsedBytes:  total - available,
		FreeBytes:  available,
	}
}

// loadWatchMeter is the single load-to-capacity projection used by collector
// and daemon-published snapshots. A non-positive CPU count carries no capacity
// and therefore cannot produce a meaningful gauge.
func loadWatchMeter(load float64, numCPU int) *web.WatchMeter {
	usedPct, ok := loadUsedPercent(load, numCPU)
	if !ok {
		return nil
	}
	return &web.WatchMeter{
		Kind:    checks.CheckTypeLoad,
		UsedPct: usedPct,
		Load:    load,
		NumCPU:  numCPU,
	}
}

// loadUsedPercent is the canonical load-to-CPU-capacity conversion shared by
// host overview metrics and load watches. A non-positive CPU count has no
// meaningful capacity.
func loadUsedPercent(load float64, numCPU int) (float64, bool) {
	if numCPU <= 0 {
		return 0, false
	}
	return load / float64(numCPU) * metrics.PercentScale, true
}

// storageWatchInfo returns the latest storage result published by the daemon.
// It deliberately never samples mounts or filesystems in the web request.
func (b *WebBackend) storageWatchInfo(w *webWatch) *web.StorageWatchInfo {
	if w == nil || w.check == nil || b.watchSnapshots == nil {
		return nil
	}
	var latest CheckSnapshot
	found := false
	for _, snap := range b.watchSnapshots.Get(w.name, w.checkType) {
		if !b.watchSnapshotCurrent(w, snap) || (found && !snap.At.After(latest.At)) {
			continue
		}
		latest, found = snap, true
	}
	if !found {
		return nil
	}
	path := cfgval.String(w.check[checks.CheckKeyPath])
	if path == "" {
		return nil
	}
	return storageWatchInfoFromSnapshot(path, latest)
}

func storageWatchInfoFromSnapshot(path string, snap CheckSnapshot) *web.StorageWatchInfo {
	data := snap.Data
	if snapshotPath := cfgval.String(data[checks.DataKeyPath]); snapshotPath != "" {
		path = snapshotPath
	}
	info := &web.StorageWatchInfo{
		Path:             path,
		Mounted:          cfgval.Bool(data[checks.DataKeyMounted]),
		MountPoint:       cfgval.String(data[checks.DataKeyMountPoint]),
		Device:           cfgval.String(data[checks.DataKeyDevice]),
		FileSystem:       cfgval.String(data[checks.DataKeyFSType]),
		TotalBytes:       snapshotUint(data, checks.DataKeyTotalBytes),
		UsedBytes:        snapshotUint(data, checks.DataKeyUsedBytes),
		FreeBytes:        snapshotUint(data, checks.DataKeyFreeBytes),
		UsedPct:          snapshotFloat(data, checks.DataKeyUsedPct),
		FreePct:          snapshotFloat(data, checks.DataKeyFreePct),
		InodesTotal:      snapshotUint(data, checks.DataKeyInodesTotal),
		InodesFree:       snapshotUint(data, checks.DataKeyInodesFree),
		InodesUsedPct:    snapshotFloat(data, checks.DataKeyInodesUsedPct),
		InodesFreePct:    snapshotFloat(data, checks.DataKeyInodesFreePct),
		SampleError:      cfgval.String(data[checks.DataKeySampleError]),
		MountSampleError: cfgval.String(data[checks.DataKeyMountSampleError]),
	}
	if options := cfgval.String(data[checks.DataKeyOptions]); options != "" {
		info.Options = strings.Split(options, ",")
	}
	if info.UsedBytes == 0 && info.TotalBytes >= info.FreeBytes {
		info.UsedBytes = info.TotalBytes - info.FreeBytes
	}
	if info.Mounted && info.MountPoint == "" {
		info.MountPoint = path
	}
	return info
}

func snapshotUint(data map[string]any, key string) uint64 {
	v, _ := uintField(data[key])
	return v
}

func snapshotFloat(data map[string]any, key string) float64 {
	v, _ := cfgval.Float(data[key])
	return v
}

func storageMountExpectation(check map[string]any) (bool, bool) {
	v, ok := check[checks.CheckKeyMounted].(bool)
	return v, ok
}

func storageUsagePredicatesConfigured(check map[string]any) bool {
	for _, field := range checks.StoragePredFields {
		if _, ok := check[field]; ok {
			return true
		}
	}
	return false
}

// uintField reads a non-negative numeric value from persisted check data,
// which may arrive as any of the numeric types the state layer round-trips.
func uintField(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint64:
		return n, true
	case int:
		if n >= 0 {
			return uint64(n), true
		}
	case int64:
		if n >= 0 {
			return uint64(n), true
		}
	case float64:
		if n >= 0 {
			return uint64(n), true
		}
	}
	return 0, false
}

// watchReadingIntMetricValue renders an integer reading through the canonical
// value formatter, so grouped counts and IEC byte values read identically to
// event messages.
func watchReadingIntMetricValue(value int64, unit string) string {
	return checks.FormatDisplayValueWithUnit(checks.DataKeyValue, value, unit)
}

// watchReadingMetricValue renders a float reading. Default-precision readings
// (and every byte count/rate) go through the canonical value formatter; the
// readings that need explicit precision — clock offsets (3/6 decimals), await
// tenths, rebuild progress — keep their fixed fmt rendering, since the
// canonical formatter caps at two decimals and trims trailing zeros.
func watchReadingMetricValue(value float64, decimals int, unit string) string {
	if decimals == watchReadingDefaultMetricDecimals || unit == metrics.MetricUnitBytes || unit == metrics.MetricUnitBytesPerSecond {
		return checks.FormatDisplayValueWithUnit(checks.DataKeyValue, value, unit)
	}
	if unit == "" {
		return fmt.Sprintf("%.*f", decimals, value)
	}
	if unit == metrics.MetricUnitPercent {
		return fmt.Sprintf("%.*f%s", decimals, value, unit)
	}
	return fmt.Sprintf("%.*f %s", decimals, value, unit)
}
