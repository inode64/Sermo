package diag

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"sermo/internal/config"
)

// diagService diagnoses one service's per-check intervals and referenced paths.
// Disabled services and ones with resolution errors (already reported by the
// config diagnostics) are skipped.
func diagService(b *builder, cfg *config.Config, name string, global time.Duration, host Host) {
	doc := cfg.Services[name]
	if doc == nil || disabled(doc.Body) {
		return
	}
	resolved, errs := cfg.Resolve(name)
	if len(errs) > 0 || resolved.Tree == nil {
		return
	}

	resolution := global
	if d := parseDuration(resolved.Tree["interval"]); d > 0 {
		resolution = d
	}
	section, _ := resolved.Tree["checks"].(map[string]any)
	for _, cn := range slices.Sorted(maps.Keys(section)) {
		entry, ok := section[cn].(map[string]any)
		if !ok {
			continue
		}
		scope := fmt.Sprintf("service %s check %s", name, cn)
		if d := parseDuration(entry["interval"]); d > 0 {
			checkAlignment(b, scope, d, resolution)
		}
		diagCheckResources(b, scope, entry, host)
	}
}

// diagWatches diagnoses host watches: interval alignment and referenced
// interfaces, files/directories and mount points.
func diagWatches(b *builder, cfg *config.Config, global time.Duration, host Host) {
	watches, _ := cfg.Global.Raw["watches"].(map[string]any)
	for _, name := range slices.Sorted(maps.Keys(watches)) {
		entry, ok := watches[name].(map[string]any)
		if !ok {
			continue
		}
		if v, ok := entry["enabled"].(bool); ok && !v {
			continue
		}
		scope := "watch " + name
		if d := parseDuration(entry["interval"]); d > 0 {
			checkAlignment(b, scope, d, global)
		}
		check, _ := entry["check"].(map[string]any)
		if check == nil {
			continue
		}
		switch str(check["type"]) {
		case "net":
			if iface := str(check["interface"]); iface != "" && !host.InterfaceExists(iface) {
				b.add(LevelWarning, scope, "network interface %q does not exist", iface)
			}
		case "file":
			if p := str(check["path"]); p != "" && !host.PathExists(p) {
				b.add(LevelWarning, scope, "path %q does not exist", p)
			}
		case "disk":
			diagDiskResources(b, scope, check, host)
		}
	}
}

// diagCheckResources flags missing paths referenced by a service check.
func diagCheckResources(b *builder, scope string, entry map[string]any, host Host) {
	switch str(entry["type"]) {
	case "disk":
		diagDiskResources(b, scope, entry, host)
	case "count":
		if p := str(entry["path"]); p != "" && !host.PathExists(p) {
			b.add(LevelWarning, scope, "directory %q does not exist", p)
		}
	}
}

// diagDiskResources flags a disk check's path when it is missing, and a configured
// mount that is not currently mounted.
func diagDiskResources(b *builder, scope string, fields map[string]any, host Host) {
	p := str(fields["path"])
	if p == "" {
		return
	}
	if !host.PathExists(p) {
		b.add(LevelWarning, scope, "path %q does not exist", p)
		return
	}
	if hasMountCondition(fields) && !host.IsMountPoint(p) {
		b.add(LevelWarning, scope, "%q has mount conditions but is not currently a mount point", p)
	}
}

func hasMountCondition(fields map[string]any) bool {
	if _, ok := fields["mounted"]; ok {
		return true
	}
	return str(fields["fstype"]) != "" || str(fields["device"]) != "" || fields["options"] != nil
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
		b.add(LevelWarning, scope, "interval %s is below the %s resolution; it will run every cycle", d, resolution)
	case time.Duration(n)*resolution != d:
		b.add(LevelWarning, scope, "interval %s is not a multiple of the %s resolution; it will run every %s", d, resolution, time.Duration(n)*resolution)
	}
}

func disabled(body map[string]any) bool {
	v, ok := body["enabled"].(bool)
	return ok && !v
}

func parseDuration(v any) time.Duration {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
