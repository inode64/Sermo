package diag

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
)

const (
	diagScopeServiceCheckFormat = "service %s check %s"
	diagScopeWatchPrefix        = "watch "
	diagMessagePathMissing      = "path %q does not exist"
)

// diagService diagnoses one service's per-check intervals and referenced paths.
// Disabled services and ones with resolution errors (already reported by the
// config diagnostics) are skipped.
func diagService(b *builder, cfg *config.Config, name string, global time.Duration, host Host) {
	doc := cfg.Services[name]
	if doc == nil || cfgval.Disabled(doc.Body) {
		return
	}
	resolved, errs := cfg.Resolve(name)
	if len(errs) > 0 || resolved.Tree == nil {
		return
	}

	resolution := global
	if d := cfgval.Duration(resolved.Tree[config.EntryKeyInterval]); d > 0 {
		resolution = d
	}
	section, _ := resolved.Tree[config.SectionChecks].(map[string]any)
	for _, cn := range slices.Sorted(maps.Keys(section)) {
		entry, ok := section[cn].(map[string]any)
		if !ok {
			continue
		}
		scope := fmt.Sprintf(diagScopeServiceCheckFormat, name, cn)
		if d := cfgval.Duration(entry[config.EntryKeyInterval]); d > 0 {
			checkAlignment(b, scope, d, resolution)
		}
		diagCheckResources(b, scope, entry, host)
	}
}

// diagWatches diagnoses host watches: interval alignment and referenced
// interfaces, files/directories and mount points.
func diagWatches(b *builder, cfg *config.Config, global time.Duration, host Host) {
	watches, _ := cfg.ResolveWatches()
	for _, name := range slices.Sorted(maps.Keys(watches)) {
		entry, ok := watches[name].(map[string]any)
		if !ok {
			continue
		}
		if cfgval.Disabled(entry) {
			continue
		}
		if config.MonitorMode(entry) == config.MonitorDisabled {
			continue
		}
		scope := diagScopeWatchPrefix + name
		if d := cfgval.Duration(entry[config.EntryKeyInterval]); d > 0 {
			checkAlignment(b, scope, d, global)
		}
		check, _ := entry[config.WatchKeyCheck].(map[string]any)
		if check == nil {
			continue
		}
		switch cfgval.AsString(check[checks.CheckKeyType]) {
		case checks.CheckTypeNet:
			warnMissingInterface(b, scope, check, host)
		case checks.CheckTypeFile:
			if p := cfgval.AsString(check[checks.CheckKeyPath]); p != "" && !host.PathExists(p) {
				b.addf(LevelWarning, scope, diagMessagePathMissing, p)
			}
		default:
			// Every single-shot check type shares the same resource probes as a
			// service's checks: section (unified check types).
			diagCheckResources(b, scope, check, host)
		}
	}
}

// diagCheckResources flags host resources referenced by a single-shot check
// that do not exist on this host. Shared by service checks and host watches.
func diagCheckResources(b *builder, scope string, entry map[string]any, host Host) {
	switch cfgval.AsString(entry[checks.CheckKeyType]) {
	case checks.CheckTypeStorage:
		diagStorageResources(b, scope, entry, host)
	case checks.CheckTypeCount:
		if p := cfgval.AsString(entry[checks.CheckKeyPath]); p != "" && !host.PathExists(p) {
			b.addf(LevelWarning, scope, "directory %q does not exist", p)
		}
	case checks.CheckTypeDiskIO:
		if dev := cfgval.AsString(entry[checks.CheckKeyDevice]); dev != "" && !host.PathExists(checks.SysBlockPath+"/"+dev) {
			b.addf(LevelWarning, scope, "block device %q does not exist (no /sys/class/block entry)", dev)
		}
	case checks.CheckTypeHdparm, checks.CheckTypeSmart:
		if dev := cfgval.AsString(entry[checks.CheckKeyDevice]); dev != "" && !host.PathExists(dev) {
			b.addf(LevelWarning, scope, "device %q does not exist", dev)
		}
	case checks.CheckTypeRoute:
		warnMissingInterface(b, scope, entry, host)
	case checks.CheckTypePressure:
		if res := cfgval.AsString(entry[checks.CheckKeyResource]); res != "" && !host.PathExists(checks.ProcPressureRootPath+"/"+res) {
			b.addf(LevelWarning, scope, "kernel exposes no /proc/pressure/%s (CONFIG_PSI off?); this check will never fire", res)
		}
	}
}

func warnMissingInterface(b *builder, scope string, entry map[string]any, host Host) {
	if iface := cfgval.AsString(entry[checks.CheckKeyInterface]); iface != "" && !host.InterfaceExists(iface) {
		b.addf(LevelWarning, scope, "network interface %q does not exist", iface)
	}
}

// diagStorageResources flags a storage check's path when it is missing, and a configured
// mount that is not currently mounted.
func diagStorageResources(b *builder, scope string, fields map[string]any, host Host) {
	p := cfgval.AsString(fields[checks.CheckKeyPath])
	if p == "" {
		return
	}
	if !host.PathExists(p) {
		b.addf(LevelWarning, scope, diagMessagePathMissing, p)
		return
	}
	if hasMountCondition(fields) && !host.IsMountPoint(p) {
		b.addf(LevelWarning, scope, "%q has mount conditions but is not currently a mount point", p)
	}
}

// hasMountCondition mirrors the storage-check schema: `mounted` is the only
// mount condition (config validation rejects fstype/device/options).
func hasMountCondition(fields map[string]any) bool {
	_, ok := fields[checks.CheckKeyMounted]
	return ok
}

// checkAlignment warns when a per-check interval is below the resolution or not an
// exact multiple of it (mirrors the daemon's startup rounding).
func checkAlignment(b *builder, scope string, d, resolution time.Duration) {
	if resolution <= 0 {
		return
	}
	n := int(math.Round(float64(d) / float64(resolution)))
	switch {
	case n < 1:
		b.addf(LevelWarning, scope, "interval %s is below the %s resolution; it will run every cycle", d, resolution)
	case time.Duration(n)*resolution != d:
		b.addf(LevelWarning, scope, "interval %s is not a multiple of the %s resolution; it will run every %s", d, resolution, time.Duration(n)*resolution)
	}
}
