package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"sermo/internal/config"
	"sermo/internal/state"
)

// defaultSLASeriesWindow is the series lookback used when --since is omitted.
const defaultSLASeriesWindow = 24 * time.Hour

// runSLA reports per-service availability over rolling windows (hour..year),
// computed from the per-cycle samples the daemon records. `sla` reports every
// configured service; `sla SERVICE` reports a single one. A window with no
// observed cycles reads "n/a" rather than 0%.
//
// With --series it instead emits SERVICE's stored per-minute availability series
// over --since (default 24h) — the raw time series a graph is built from. Minutes
// where the service was not monitored (Sermo down, or the service paused or
// disabled) are absent from the series, never counted as downtime.
func (a App) runSLA(opts options) int {
	if len(opts.args) > 1 {
		return a.commandUsageError("sla", "sla accepts at most one service name")
	}
	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}

	if opts.series {
		return a.runSLASeries(opts, cfg)
	}

	var services []string
	if s := opts.service(); s != "" {
		if _, ok := cfg.Services[s]; !ok {
			return a.fail(opts, fmt.Sprintf("unknown service %q", s))
		}
		services = []string{s}
	} else {
		services = sortedUnique(cfg.Services)
	}

	store, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		return a.fail(opts, fmt.Sprintf("sla failed: %v", err))
	}
	defer store.Close()

	now := time.Now()
	reports := make([]serviceSLA, 0, len(services))
	for _, name := range services {
		values, err := store.SLAReport(name, now)
		if err != nil {
			return a.fail(opts, fmt.Sprintf("sla %s failed: %v", name, err))
		}
		reports = append(reports, serviceSLA{Service: name, Windows: values})
	}

	if opts.json {
		a.writeSLAJSON(reports)
	} else {
		a.writeSLATable(reports)
	}
	return exitSuccess
}

// runSLASeries emits one service's stored per-minute availability series, the
// data a future graph plots.
func (a App) runSLASeries(opts options, cfg *config.Config) int {
	service := opts.service()
	if service == "" {
		return a.commandUsageError("sla", "sla --series requires a service name")
	}
	if _, ok := cfg.Services[service]; !ok {
		return a.fail(opts, fmt.Sprintf("unknown service %q", service))
	}

	window := opts.since
	if window <= 0 {
		window = defaultSLASeriesWindow
	}

	store, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		return a.fail(opts, fmt.Sprintf("sla failed: %v", err))
	}
	defer store.Close()

	now := time.Now()
	points, err := store.SLASeries(service, now.Add(-window), now)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("sla %s failed: %v", service, err))
	}

	if opts.json {
		a.writeSLASeriesJSON(service, window, points)
	} else {
		a.writeSLASeriesTable(service, points)
	}
	return exitSuccess
}

type serviceSLA struct {
	Service string
	Windows []state.SLAValue
}

func (a App) writeSLAJSON(reports []serviceSLA) {
	out := make([]map[string]any, 0, len(reports))
	for _, r := range reports {
		windows := make(map[string]any, len(r.Windows))
		for _, v := range r.Windows {
			windows[v.Window] = slaValueJSON(v)
		}
		out = append(out, map[string]any{"service": r.Service, "windows": windows})
	}
	writeJSON(a.Stdout, map[string]any{"sla": out})
}

func slaValueJSON(v state.SLAValue) map[string]any {
	entry := map[string]any{"up": v.Up, "total": v.Total, "ratio": nil}
	if ratio, ok := v.Ratio(); ok {
		entry["ratio"] = ratio
	}
	return entry
}

func (a App) writeSLATable(reports []serviceSLA) {
	if len(reports) == 0 {
		fmt.Fprintln(a.Stdout, "no services")
		return
	}
	cols := make([]string, 0, len(state.SLAWindows)+1)
	cols = append(cols, "SERVICE")
	for _, w := range state.SLAWindows {
		cols = append(cols, strings.ToUpper(w.Name))
	}
	fmt.Fprintln(a.Stdout, strings.Join(cols, "\t"))

	for _, r := range reports {
		row := make([]string, 0, len(r.Windows)+1)
		row = append(row, r.Service)
		for _, v := range r.Windows {
			row = append(row, formatSLA(v))
		}
		fmt.Fprintln(a.Stdout, strings.Join(row, "\t"))
	}
}

// formatSLA renders one window as a percentage, or "n/a" when it has no data.
func formatSLA(v state.SLAValue) string {
	ratio, ok := v.Ratio()
	if !ok {
		return "n/a"
	}
	return fmt.Sprintf("%.2f%%", ratio*100)
}

func (a App) writeSLASeriesTable(service string, points []state.SLAPoint) {
	if len(points) == 0 {
		fmt.Fprintf(a.Stdout, "no samples for %s in range (service unmonitored or Sermo not running)\n", service)
		return
	}
	fmt.Fprintln(a.Stdout, "TIME\tUP\tTOTAL\tSLA")
	for _, p := range points {
		sla := "n/a"
		if ratio, ok := slaPointRatio(p); ok {
			sla = fmt.Sprintf("%.2f%%", ratio*100)
		}
		fmt.Fprintf(a.Stdout, "%s\t%d\t%d\t%s\n", p.Start.Format(time.RFC3339), p.Up, p.Total, sla)
	}
}

func (a App) writeSLASeriesJSON(service string, window time.Duration, points []state.SLAPoint) {
	series := make([]map[string]any, 0, len(points))
	for _, p := range points {
		series = append(series, slaPointJSON(p))
	}
	writeJSON(a.Stdout, map[string]any{
		"service": service,
		"since":   window.String(),
		"series":  series,
	})
}

func slaPointJSON(p state.SLAPoint) map[string]any {
	entry := map[string]any{"start": p.Start.Format(time.RFC3339), "up": p.Up, "total": p.Total, "ratio": nil}
	if ratio, ok := slaPointRatio(p); ok {
		entry["ratio"] = ratio
	}
	return entry
}

func slaPointRatio(p state.SLAPoint) (float64, bool) {
	if p.Total <= 0 {
		return 0, false
	}
	return float64(p.Up) / float64(p.Total), true
}
