package cli

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"sermo/internal/app"
	"sermo/internal/config"
	"sermo/internal/state"
)

// defaultSLASeriesWindow is the series lookback used when --since is omitted.
const defaultSLASeriesWindow = state.DefaultSeriesWindow

// cliUnknownSLATargetFormat names both places an availability series can come
// from, so a typo does not read as "this service does not exist" when the
// operator meant a watch.
const cliUnknownSLATargetFormat = "unknown service or availability watch %q"

// runSLA reports per-service availability over rolling windows (hour..year),
// computed from the per-cycle samples the daemon records. `sla` reports every
// configured service; `sla SERVICE` reports a single one. A window with no
// observed cycles reads "n/a" rather than 0%.
//
// With --series it instead emits SERVICE's stored per-minute availability series
// over --since (default 24h) — the raw time series a graph is built from. Neither
// form turns daemon downtime or missing data into observed downtime.
func (a App) runSLA(ctx context.Context, opts options) int {
	if len(opts.args) > 1 {
		return a.commandUsageError(commandSLA, "sla accepts at most one service or watch name")
	}
	cfg, code := a.loadConfig(opts)
	if cfg == nil {
		return code
	}

	if opts.series {
		return a.runSLASeries(ctx, opts, cfg)
	}

	return runWindowsReport(ctx, a, opts, cfg,
		func(s *state.Store, name string, now time.Time) ([]state.SLAValue, error) {
			return s.SLAReport(name, now)
		},
		a.writeSLAJSON, a.writeSLATable)
}

// runWindowsReport loads each service's per-window values via report and
// renders them as JSON or a table.
func runWindowsReport[V any](ctx context.Context, a App, opts options, cfg *config.Config,
	report func(*state.Store, string, time.Time) ([]V, error),
	writeJSON, writeTable func([]serviceWindows[V])) int {
	targets, code := a.slaTargets(opts, cfg)
	if code != exitSuccess {
		return code
	}

	return withStateStore(ctx, cfg, func(err error) int {
		return a.fail(opts, fmt.Sprintf("sla failed: %v", err))
	}, func(store *state.Store) int {
		now := time.Now()
		reports := make([]serviceWindows[V], 0, len(targets))
		for _, target := range targets {
			values, err := report(store, target.key, now)
			if err != nil {
				return a.fail(opts, fmt.Sprintf("sla %s failed: %v", target.name, err))
			}
			reports = append(reports, serviceWindows[V]{Service: target.name, Windows: values})
		}

		if opts.json {
			writeJSON(reports)
		} else {
			writeTable(reports)
		}
		return exitSuccess
	})
}

// slaTarget is one thing an availability series is kept for. name is what the
// operator types and reads; key is how the series is keyed in the store, which
// for a host watch carries the "watch:" namespace so a watch and a service of
// the same name never share a series.
type slaTarget struct {
	name string
	key  string
}

// slaTargets resolves the targets to report: one named on the command line, or
// every configured service plus every host watch whose check asserts
// availability. A name is looked up as a service first, so an existing service
// name never changes meaning.
func (a App) slaTargets(opts options, cfg *config.Config) ([]slaTarget, int) {
	if name := opts.service(); name != "" {
		if canonical, ok := cfg.CanonicalServiceName(name); ok {
			return []slaTarget{{name: canonical, key: canonical}}, exitSuccess
		}
		if slices.Contains(cfg.AvailabilityWatchNames(), name) {
			return []slaTarget{{name: name, key: app.WatchMonitorKey(name)}}, exitSuccess
		}
		return nil, a.fail(opts, fmt.Sprintf(cliUnknownSLATargetFormat, name))
	}
	services := slices.Sorted(maps.Keys(cfg.Services))
	services = slices.DeleteFunc(services, func(name string) bool { return name == "" })
	targets := make([]slaTarget, 0, len(services))
	for _, service := range services {
		targets = append(targets, slaTarget{name: service, key: service})
	}
	for _, watch := range cfg.AvailabilityWatchNames() {
		targets = append(targets, slaTarget{name: watch, key: app.WatchMonitorKey(watch)})
	}
	return targets, exitSuccess
}

// runSLASeries emits one service's stored per-minute availability series, the
// data a future graph plots.
func (a App) runSLASeries(ctx context.Context, opts options, cfg *config.Config) int {
	if opts.service() == "" {
		return a.commandUsageError(commandSLA, "sla --series requires a service or watch name")
	}
	targets, code := a.slaTargets(opts, cfg)
	if code != exitSuccess {
		return code
	}
	// A named lookup resolves to exactly one target or fails above; the guard
	// keeps that an assertion rather than an assumption.
	if len(targets) != 1 {
		return a.fail(opts, fmt.Sprintf(cliUnknownSLATargetFormat, opts.service()))
	}
	target := targets[0]

	window := opts.since
	if window <= 0 {
		window = defaultSLASeriesWindow
	}

	return withStateStore(ctx, cfg, func(err error) int {
		return a.fail(opts, fmt.Sprintf("sla failed: %v", err))
	}, func(store *state.Store) int {
		now := time.Now()
		points, err := store.SLASeries(target.key, now.Add(-window), now)
		if err != nil {
			return a.fail(opts, fmt.Sprintf("sla %s failed: %v", target.name, err))
		}

		if opts.json {
			a.writeSLASeriesJSON(target.name, window, points)
		} else {
			a.writeSLASeriesTable(target.name, points, store.SeriesResolution(now.Add(-window), now))
		}
		return exitSuccess
	})
}

// serviceWindows pairs one service with its per-window availability values.
type serviceWindows[V any] struct {
	Service string
	Windows []V
}

func (a App) writeSLAJSON(reports []serviceWindows[state.SLAValue]) {
	writeSLAWindowJSON(a, cliJSONKeySLA, reports,
		func(v state.SLAValue) (string, map[string]any) { return v.Window, slaValueJSON(v) })
}

// writeSLAWindowJSON renders the {top: [{service, windows}]} JSON envelope,
// mirroring writeSLAWindowTable for the table form.
func writeSLAWindowJSON[V any](a App, topKey string, reports []serviceWindows[V], window func(V) (string, map[string]any)) {
	out := make([]map[string]any, 0, len(reports))
	for _, r := range reports {
		windows := make(map[string]any, len(r.Windows))
		for _, v := range r.Windows {
			name, entry := window(v)
			windows[name] = entry
		}
		out = append(out, map[string]any{cliJSONKeyService: r.Service, cliJSONKeyWindows: windows})
	}
	writeJSON(a.Stdout, map[string]any{topKey: out})
}

func slaValueJSON(v state.SLAValue) map[string]any {
	entry := map[string]any{
		cliJSONKeyUp:          v.Up,
		cliJSONKeyTotal:       v.Total,
		cliJSONKeyDownBuckets: v.DownBuckets,
		cliJSONKeyRatio:       nil,
	}
	if ratio, ok := v.Ratio(); ok {
		entry[cliJSONKeyRatio] = ratio
	}
	return entry
}

func (a App) writeSLATable(reports []serviceWindows[state.SLAValue]) {
	writeSLAWindowTable(a, reports, state.SLAValue.PercentText)
}

// writeSLAWindowTable renders one TARGET + per-SLA-window availability table.
// A target is a configured service or an availability host watch.
func writeSLAWindowTable[V any](a App, reports []serviceWindows[V], format func(V) string) {
	if len(reports) == 0 {
		fmt.Fprintln(a.Stdout, "no targets")
		return
	}
	cols := make([]string, 0, len(state.SLAWindows)+1)
	cols = append(cols, "TARGET")
	for _, window := range state.SLAWindows {
		cols = append(cols, strings.ToUpper(window.Name))
	}
	fmt.Fprintln(a.Stdout, strings.Join(cols, "\t"))
	for _, report := range reports {
		row := make([]string, 0, len(report.Windows)+1)
		row = append(row, report.Service)
		for _, window := range report.Windows {
			row = append(row, format(window))
		}
		fmt.Fprintln(a.Stdout, strings.Join(row, "\t"))
	}
}

func (a App) writeSLASeriesTable(service string, points []state.SLAPoint, step time.Duration) {
	if len(points) == 0 {
		fmt.Fprintf(a.Stdout, "no samples for %s in range (service unmonitored or Sermo not running)\n", service)
		return
	}
	// The bucket span depends on the archive the window resolved to, so state it
	// rather than letting the reader assume the per-minute rows this used to print.
	fmt.Fprintf(a.Stdout, "resolution: %s per row\n", step)
	fmt.Fprintln(a.Stdout, "TIME\tUP\tTOTAL\tSLA\tAFFECTED_MIN")
	for _, p := range points {
		fmt.Fprintf(a.Stdout, "%s\t%d\t%d\t%s\t%d\n",
			p.Start.Format(time.RFC3339), p.Up, p.Total, p.PercentText(), p.DownBuckets)
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
	entry := map[string]any{
		cliJSONKeyStart:       p.Start.Format(time.RFC3339),
		cliJSONKeyUp:          p.Up,
		cliJSONKeyTotal:       p.Total,
		cliJSONKeyDownBuckets: p.DownBuckets,
		cliJSONKeyRatio:       nil,
	}
	if ratio, ok := p.Ratio(); ok {
		entry[cliJSONKeyRatio] = ratio
	}
	return entry
}
