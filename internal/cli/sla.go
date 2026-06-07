package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"sermo/internal/state"
)

// runSLA reports per-service availability over rolling windows (hour..year),
// computed from the per-cycle samples the daemon records. `sla` reports every
// configured service; `sla SERVICE` reports a single one. A window with no
// observed cycles reads "n/a" rather than 0%.
func (a App) runSLA(opts options) int {
	cfg, code := a.loadConfig(opts)
	if code != exitSuccess {
		return code
	}

	var services []string
	if s := opts.service(); s != "" {
		if _, ok := cfg.Services[s]; !ok {
			a.reportError(opts, fmt.Sprintf("unknown service %q", s))
			return exitRuntimeError
		}
		services = []string{s}
	} else {
		services = sortedUnique(cfg.Services)
	}

	store, err := state.Open(filepath.Join(cfg.Global.StateDir(), state.Filename))
	if err != nil {
		a.reportError(opts, fmt.Sprintf("sla failed: %v", err))
		return exitRuntimeError
	}
	defer store.Close()

	now := time.Now()
	reports := make([]serviceSLA, 0, len(services))
	for _, name := range services {
		values, err := store.SLAReport(name, now)
		if err != nil {
			a.reportError(opts, fmt.Sprintf("sla %s failed: %v", name, err))
			return exitRuntimeError
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

type serviceSLA struct {
	Service string
	Windows []state.SLAValue
}

func (a App) writeSLAJSON(reports []serviceSLA) {
	out := make([]map[string]any, 0, len(reports))
	for _, r := range reports {
		windows := make(map[string]any, len(r.Windows))
		for _, v := range r.Windows {
			entry := map[string]any{"up": v.Up, "total": v.Total, "ratio": nil}
			if ratio, ok := v.Ratio(); ok {
				entry["ratio"] = ratio
			}
			windows[v.Window] = entry
		}
		out = append(out, map[string]any{"service": r.Service, "windows": windows})
	}
	writeJSON(a.Stdout, map[string]any{"sla": out})
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
