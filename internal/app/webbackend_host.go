package app

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"sermo/internal/config"
	"sermo/internal/metrics"
	"sermo/internal/web"
)

const (
	procUptimePath         = "/proc/uptime"
	procUptimeValueIndex   = 0
	procUptimeFloatBits    = 64
	osReleasePrettyNameKey = "PRETTY_NAME="
	osReleaseValueTrimSet  = `"'`
)

// hostUptime returns how long the host/server has been running since boot,
// read natively from /proc/uptime. The second return is false when the host
// uptime is unavailable (e.g. the file is missing on non-Linux systems).
func hostUptime() (time.Duration, bool) {
	data, err := os.ReadFile(procUptimePath)
	if err != nil {
		return 0, false
	}
	return parseProcUptime(data)
}

// parseProcUptime extracts the boot-relative uptime from the contents of
// /proc/uptime, whose first whitespace-separated field is the number of
// seconds (a float) since boot. It returns false when the value is missing or
// unparseable.
func parseProcUptime(data []byte) (time.Duration, bool) {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	secs, err := strconv.ParseFloat(fields[procUptimeValueIndex], procUptimeFloatBits)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}

// osPrettyName returns a human-friendly OS label (PRETTY_NAME from os-release on
// Linux, e.g. "Debian GNU/Linux 12 (bookworm)"), falling back to runtime.GOOS.
func osPrettyName() string {
	for _, path := range config.OSReleasePaths() {
		if data, err := os.ReadFile(path); err == nil {
			if name := parseOSReleasePrettyName(data); name != "" {
				return name
			}
		}
	}
	return runtime.GOOS
}

// parseOSReleasePrettyName extracts the (unquoted) PRETTY_NAME value from
// os-release content, or "" when absent. Pure, so it is testable without the
// host files.
func parseOSReleasePrettyName(data []byte) string {
	for line := range strings.SplitSeq(string(data), appLineSeparator) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), osReleasePrettyNameKey); ok {
			if name := strings.Trim(v, osReleaseValueTrimSet); name != "" {
				return name
			}
		}
	}
	return ""
}

// HostMetrics returns the current host-level readings from the collector.
func (b *WebBackend) HostMetrics(_ context.Context) []web.HostMetric {
	if b.collector == nil {
		return nil
	}
	snap := b.collector.SampleSystem()
	if len(snap) == 0 {
		return nil
	}

	out := make([]web.HostMetric, 0, len(snap))
	order := []string{
		metrics.MetricLoad1,
		metrics.MetricLoad5,
		metrics.MetricLoad15,
		metrics.MetricTotalCPU,
		metrics.MetricTotalMemory,
		metrics.MetricTotalSwap,
	} // nice display order
	seen := map[string]bool{}
	for _, k := range order {
		if r, ok := snap[k]; ok {
			out = append(out, hostMetric(k, r))
			seen[k] = true
		}
	}
	for k, r := range snap { // any others the collector reported, after the ordered ones
		if !seen[k] {
			out = append(out, hostMetric(k, r))
		}
	}
	return out
}

// hostMetric maps a collector reading to the web view, applying the metric's
// display specifics: a bytes unit for memory/swap, and a 0-100% saturation
// reading for load1 (load vs logical CPUs, capacity = CPU count) so the overview
// tile can draw a bar like cpu/mem/swap. The raw load stays in Absolute.
func hostMetric(name string, r metrics.Reading) web.HostMetric {
	m := web.HostMetric{Name: name, Ready: r.Ready}
	if r.HasPercent {
		m.Percent = r.Percent
	}
	if r.HasAbsolute {
		m.Absolute = r.Absolute
	}
	if r.HasTotal {
		m.Total = r.Total
	}
	switch name {
	case metrics.MetricTotalMemory, metrics.MetricTotalSwap:
		m.Unit = metrics.MetricUnitBytes
	case metrics.MetricLoad1:
		// Only derive the per-CPU percentage from a real reading; guarding on
		// HasAbsolute (as watchMeter does) avoids fabricating Total/Percent when
		// load1 has no absolute value.
		if r.HasAbsolute {
			if ncpu := runtime.NumCPU(); ncpu > 0 {
				m.Total = float64(ncpu)
				m.Percent = r.Absolute / float64(ncpu) * metrics.PercentScale
			}
		}
	}
	return m
}
