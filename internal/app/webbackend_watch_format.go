package app

import (
	"fmt"
	"runtime"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/web"
	"slices"
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
	parts := make([]string, 0, min(len(samples), processPIDListLimit)+1)
	for i, sample := range samples {
		if i >= processPIDListLimit {
			break
		}
		parts = append(parts, strconv.Itoa(sample.PID))
	}
	if extra := len(samples) - processPIDListLimit; extra > 0 {
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
		used, total, free, ok := byteUsage(r)
		if !ok {
			return nil
		}
		return &web.WatchMeter{
			Kind:       metrics.MetricMemory,
			UsedPct:    r.Percent,
			TotalBytes: total,
			UsedBytes:  used,
			FreeBytes:  free,
		}
	case checks.CheckTypeLoad:
		r, ok := system[metrics.MetricLoad1]
		if !ok || !r.HasAbsolute {
			return nil
		}
		ncpu := runtime.NumCPU()
		pct := 0.0
		if ncpu > 0 {
			pct = r.Absolute / float64(ncpu) * metrics.PercentScale
		}
		return &web.WatchMeter{Kind: checks.CheckTypeLoad, UsedPct: pct, Load: r.Absolute, NumCPU: ncpu}
	}
	return nil
}

// countMeter builds a count-vs-limit gauge (fds, pids) as a percentage of the
// kernel maximum. nil when the limit is unknown (limit == 0), so the meter is
// simply absent rather than dividing by zero.
func countMeter(kind string, count, limit uint64) *web.WatchMeter {
	if limit == 0 {
		return nil
	}
	return &web.WatchMeter{
		Kind:    kind,
		UsedPct: float64(count) / float64(limit) * metrics.PercentScale,
		Count:   count,
		Max:     limit,
	}
}

func storageWatchInfo(w *webWatch, b *WebBackend) *web.StorageWatchInfo {
	if w == nil || w.check == nil {
		return nil
	}
	path := cfgval.String(w.check[checks.CheckKeyPath])
	if path == "" {
		return nil
	}
	info := &web.StorageWatchInfo{Path: path}

	mountSampler := b.mountSampler
	if mountSampler == nil {
		mountSampler = checks.DefaultMounts
	}
	mounts, err := mountSampler()
	if err != nil {
		info.MountSampleError = err.Error()
	} else {
		mount := checks.MountForPath(mounts, path)
		if _, ok := storageMountExpectation(w.check); ok {
			mount = checks.MountAtPath(mounts, path)
		}
		if mount != nil {
			info.Mounted = true
			info.MountPoint = mount.MountPoint
			info.Device = mount.Device
			info.FileSystem = mount.FSType
			info.Options = slices.Clone(mount.Options)
			if storageUsagePredicatesConfigured(w.check) {
				info.OpenFiles = b.openFilesByMountCached(mounts)[mount.MountPoint]
			}
		}
		if _, ok := storageMountExpectation(w.check); ok && (!info.Mounted || !storageUsagePredicatesConfigured(w.check)) {
			return info
		}
	}

	usage := b.storageUsage
	if usage == nil {
		usage = checks.DefaultStorageUsage
	}
	if st, err := usage(path); err != nil {
		info.SampleError = err.Error()
	} else {
		info.TotalBytes = st.TotalBytes
		info.FreeBytes = st.FreeBytes
		info.UsedBytes = st.UsedBytes
		if info.UsedBytes == 0 && st.TotalBytes >= st.FreeBytes {
			info.UsedBytes = st.TotalBytes - st.FreeBytes
		}
		info.UsedPct = st.UsedPct
		info.FreePct = st.FreePct
		info.InodesTotal = st.InodesTotal
		info.InodesFree = st.InodesFree
		info.InodesUsedPct = st.InodesUsedPct
		info.InodesFreePct = st.InodesFreePct
	}
	return info
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

func watchReadingIntMetricValue(value int64, unit string) string {
	if unit == "" {
		return strconv.FormatInt(value, 10)
	}
	return fmt.Sprintf("%d %s", value, unit)
}

func watchReadingMetricValue(value float64, decimals int, unit string) string {
	if unit == "" {
		return fmt.Sprintf("%.*f", decimals, value)
	}
	if unit == metrics.MetricUnitPercent {
		return fmt.Sprintf("%.*f%s", decimals, value, unit)
	}
	return fmt.Sprintf("%.*f %s", decimals, value, unit)
}
