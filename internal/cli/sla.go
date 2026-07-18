package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sermo/internal/config"
	"sermo/internal/metrics"
	"sermo/internal/state"
	"sermo/internal/units"
)

// defaultSLASeriesWindow is the series lookback used when --since is omitted.
const defaultSLASeriesWindow = state.DefaultSeriesWindow

const cliTextNotAvailable = "n/a"

// runSLA reports per-service availability over rolling windows (hour..year),
// computed from the per-cycle samples the daemon records. `sla` reports every
// configured service; `sla SERVICE` reports a single one. A window with no
// observed cycles reads "n/a" rather than 0%.
//
// With --series it instead emits SERVICE's stored per-minute availability series
// over --since (default 24h) — the raw time series a graph is built from. With
// --process-uptime it reports separately confirmed process-continuity coverage.
// Neither form turns daemon downtime or missing data into observed downtime.
func (a App) runSLA(ctx context.Context, opts options) int {
	if len(opts.args) > 1 {
		return a.commandUsageError(commandSLA, "sla accepts at most one service name")
	}
	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}

	if opts.series && opts.processUptime {
		return a.commandUsageError(commandSLA, "sla --series and --process-uptime cannot be used together")
	}
	if opts.series {
		return a.runSLASeries(ctx, opts, cfg)
	}
	if opts.processUptime {
		return a.runProcessUptime(ctx, opts, cfg)
	}

	services, code := a.slaServices(opts, cfg)
	if code != exitSuccess {
		return code
	}

	store, err := openStateStore(ctx, cfg)
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

func (a App) slaServices(opts options, cfg *config.Config) ([]string, int) {
	if service := opts.service(); service != "" {
		canonical, ok := cfg.CanonicalServiceName(service)
		if !ok {
			return nil, a.fail(opts, fmt.Sprintf(cliUnknownServiceFormat, service))
		}
		return []string{canonical}, exitSuccess
	}
	return sortedUnique(cfg.Services), exitSuccess
}

// runProcessUptime reports trusted process-continuity coverage. It remains
// separate from SLA because a process being alive cannot prove check health.
func (a App) runProcessUptime(ctx context.Context, opts options, cfg *config.Config) int {
	services, code := a.slaServices(opts, cfg)
	if code != exitSuccess {
		return code
	}
	store, err := openStateStore(ctx, cfg)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("sla failed: %v", err))
	}
	defer store.Close()

	now := time.Now()
	reports := make([]serviceProcessUptime, 0, len(services))
	for _, name := range services {
		values, err := store.ProcessUptimeReport(name, now)
		if err != nil {
			return a.fail(opts, fmt.Sprintf("sla %s failed: %v", name, err))
		}
		reports = append(reports, serviceProcessUptime{Service: name, Windows: values})
	}
	if opts.json {
		a.writeProcessUptimeJSON(reports)
	} else {
		a.writeProcessUptimeTable(reports)
	}
	return exitSuccess
}

// runSLASeries emits one service's stored per-minute availability series, the
// data a future graph plots.
func (a App) runSLASeries(ctx context.Context, opts options, cfg *config.Config) int {
	service := opts.service()
	if service == "" {
		return a.commandUsageError(commandSLA, "sla --series requires a service name")
	}
	canonical, ok := cfg.CanonicalServiceName(service)
	if !ok {
		return a.fail(opts, fmt.Sprintf(cliUnknownServiceFormat, service))
	}
	service = canonical

	window := opts.since
	if window <= 0 {
		window = defaultSLASeriesWindow
	}

	store, err := openStateStore(ctx, cfg)
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

type serviceProcessUptime struct {
	Service string
	Windows []state.ProcessUptimeWindow
}

func (a App) writeSLAJSON(reports []serviceSLA) {
	writeSLAWindowJSON(a, cliJSONKeySLA, reports,
		func(r serviceSLA) (string, []state.SLAValue) { return r.Service, r.Windows },
		func(v state.SLAValue) (string, map[string]any) { return v.Window, slaValueJSON(v) })
}

// writeSLAWindowJSON renders the {top: [{service, windows}]} JSON envelope
// shared by the availability and process-uptime reports so their shape cannot
// drift, mirroring writeSLAWindowTable for the table forms.
func writeSLAWindowJSON[R, V any](a App, topKey string, reports []R, fields func(R) (string, []V), window func(V) (string, map[string]any)) {
	out := make([]map[string]any, 0, len(reports))
	for _, r := range reports {
		service, values := fields(r)
		windows := make(map[string]any, len(values))
		for _, v := range values {
			name, entry := window(v)
			windows[name] = entry
		}
		out = append(out, map[string]any{cliJSONKeyService: service, cliJSONKeyWindows: windows})
	}
	writeJSON(a.Stdout, map[string]any{topKey: out})
}

func slaValueJSON(v state.SLAValue) map[string]any {
	entry := map[string]any{cliJSONKeyUp: v.Up, cliJSONKeyTotal: v.Total, cliJSONKeyRatio: nil}
	if ratio, ok := v.Ratio(); ok {
		entry[cliJSONKeyRatio] = ratio
	}
	return entry
}

func (a App) writeProcessUptimeJSON(reports []serviceProcessUptime) {
	writeSLAWindowJSON(a, cliJSONKeyProcessUptime, reports,
		func(r serviceProcessUptime) (string, []state.ProcessUptimeWindow) { return r.Service, r.Windows },
		func(v state.ProcessUptimeWindow) (string, map[string]any) { return v.Window, processUptimeValueJSON(v) })
}

func processUptimeValueJSON(v state.ProcessUptimeWindow) map[string]any {
	entry := map[string]any{
		cliJSONKeyCoveredSeconds: v.CoveredSeconds,
		cliJSONKeyTotalSeconds:   v.TotalSeconds,
		cliJSONKeyRatio:          nil,
	}
	if v.Known && v.TotalSeconds > 0 {
		entry[cliJSONKeyRatio] = float64(v.CoveredSeconds) / float64(v.TotalSeconds)
	}
	return entry
}

func (a App) writeSLATable(reports []serviceSLA) {
	writeSLAWindowTable(a, reports, func(r serviceSLA) (string, []state.SLAValue) { return r.Service, r.Windows }, formatSLA)
}

func (a App) writeProcessUptimeTable(reports []serviceProcessUptime) {
	writeSLAWindowTable(a, reports, func(r serviceProcessUptime) (string, []state.ProcessUptimeWindow) { return r.Service, r.Windows }, formatProcessUptime)
}

// writeSLAWindowTable renders one SERVICE + per-SLA-window table, shared by the
// availability and process-uptime reports so their layout cannot drift.
func writeSLAWindowTable[R, V any](a App, reports []R, fields func(R) (string, []V), format func(V) string) {
	if len(reports) == 0 {
		fmt.Fprintln(a.Stdout, "no services")
		return
	}
	cols := make([]string, 0, len(state.SLAWindows)+1)
	cols = append(cols, "SERVICE")
	for _, window := range state.SLAWindows {
		cols = append(cols, strings.ToUpper(window.Name))
	}
	fmt.Fprintln(a.Stdout, strings.Join(cols, "\t"))
	for _, report := range reports {
		service, windows := fields(report)
		row := make([]string, 0, len(windows)+1)
		row = append(row, service)
		for _, window := range windows {
			row = append(row, format(window))
		}
		fmt.Fprintln(a.Stdout, strings.Join(row, "\t"))
	}
}

// formatSLA renders one window as a percentage, or "n/a" when it has no data.
func formatSLA(v state.SLAValue) string {
	ratio, ok := v.Ratio()
	if !ok {
		return cliTextNotAvailable
	}
	return fmt.Sprintf("%.2f%%", ratio*metrics.PercentScale)
}

func formatProcessUptime(v state.ProcessUptimeWindow) string {
	if !v.Known || v.TotalSeconds <= 0 {
		return cliTextNotAvailable
	}
	covered := units.HumanizeDuration(time.Duration(v.CoveredSeconds) * time.Second)
	total := units.HumanizeDuration(time.Duration(v.TotalSeconds) * time.Second)
	return covered + "/" + total
}

func (a App) writeSLASeriesTable(service string, points []state.SLAPoint) {
	if len(points) == 0 {
		fmt.Fprintf(a.Stdout, "no samples for %s in range (service unmonitored or Sermo not running)\n", service)
		return
	}
	fmt.Fprintln(a.Stdout, "TIME\tUP\tTOTAL\tSLA")
	for _, p := range points {
		sla := cliTextNotAvailable
		if ratio, ok := slaPointRatio(p); ok {
			sla = fmt.Sprintf("%.2f%%", ratio*metrics.PercentScale)
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
		cliJSONKeyService: service,
		cliJSONKeySince:   window.String(),
		cliJSONKeySeries:  series,
	})
}

func slaPointJSON(p state.SLAPoint) map[string]any {
	entry := map[string]any{cliJSONKeyStart: p.Start.Format(time.RFC3339), cliJSONKeyUp: p.Up, cliJSONKeyTotal: p.Total, cliJSONKeyRatio: nil}
	if ratio, ok := slaPointRatio(p); ok {
		entry[cliJSONKeyRatio] = ratio
	}
	return entry
}

func slaPointRatio(p state.SLAPoint) (float64, bool) {
	if p.Total <= 0 {
		return 0, false
	}
	return float64(p.Up) / float64(p.Total), true
}
