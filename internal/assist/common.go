package assist

import (
	"strings"
	"time"

	"sermo/internal/config"
)

// Monitoring is the shared monitor-state + interval answer every wizard asks
// once the targets are chosen. It is injected verbatim into each generated
// entry (a watch entry or a service body) by the assistants, so the question
// flow stays identical across wizards. See docs/wizards.md.
type Monitoring struct {
	Monitor  string // config `monitor:` value: enabled | disabled | previous
	Interval string // config `interval:` value; "" inherits the global engine interval
}

// AskMonitoring asks the two questions common to every wizard: how the entry
// should be monitored on startup, and its check interval. label names what the
// answers apply to (one target, or "all selected …" in batch mode).
func (p *Prompt) AskMonitoring(label string) Monitoring {
	return Monitoring{
		Monitor:  p.AskMonitorState(label),
		Interval: p.AskInterval(""),
	}
}

// AskMonitorState asks how the generated entry should be monitored on daemon
// startup, returning the config `monitor:` value.
func (p *Prompt) AskMonitorState(label string) string {
	switch p.Choose("How should "+label+" be monitored on startup?", []string{
		"monitor (enabled)",
		"do not monitor (disabled)",
		"restore previous state",
	}) {
	case 1:
		return config.MonitorDisabled
	case 2:
		return config.MonitorPrevious
	default:
		return config.MonitorEnabled
	}
}

// AskInterval asks for a per-entry check interval, returning "" to inherit the
// global engine interval. It re-prompts on a value config validation would
// reject (mirrors askDuration but allows the blank "inherit" answer).
func (p *Prompt) AskInterval(def string) string {
	for {
		v := strings.TrimSpace(p.Ask("Check interval (blank = inherit the global interval)", def))
		if v == "" {
			return ""
		}
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return v
		}
		p.printf("  use a positive duration like 30s or 5m, or leave blank to inherit\n")
	}
}

// apply writes the monitoring answer onto a generated entry map (watch entry or
// service body): always the monitor flag, plus interval when one was given.
// Keeps the injection identical across assistants.
func (m Monitoring) apply(entry map[string]any) {
	if m.Monitor != "" {
		entry["monitor"] = m.Monitor
	}
	if m.Interval != "" {
		entry["interval"] = m.Interval
	}
}
