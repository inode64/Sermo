package assist

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"sermo/internal/config"
	"sermo/internal/rules"
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
		entry[config.EntryKeyMonitor] = m.Monitor
	}
	if m.Interval != "" {
		entry[config.EntryKeyInterval] = m.Interval
	}
}

// AskWatchDryRun asks whether a generated watch should simulate its automatic
// actions without executing them. It is only asked when the watch has an actual
// side effect to skip: a real notifier target, an inherited global notifier, or
// a native action such as then.expand/then.kill.
func (p *Prompt) AskWatchDryRun(label string, env Env, notifiers []string, hasNativeAction bool) bool {
	if !watchHasSideEffect(env, notifiers, hasNativeAction) {
		return false
	}
	return p.Confirm("Dry-run "+label+" actions first (evaluate but skip hook/non-console notify/native actions)?", false)
}

func watchHasSideEffect(env Env, notifiers []string, hasNativeAction bool) bool {
	if hasNativeAction {
		return true
	}
	if config.NotifyOptedOut(notifiers) {
		return false
	}
	if len(notifiers) == 0 {
		return len(env.DefaultNotify) > 0
	}
	return config.HasNotifyAction(notifiers)
}

func applyDryRun(entry map[string]any, dryRun bool) {
	if dryRun {
		entry[config.EntryKeyDryRun] = true
	}
}

func watchThen(notifiers []string) map[string]any {
	then := map[string]any{}
	if len(notifiers) > 0 {
		then[rules.RuleFieldNotify] = notifiers
	}
	return then
}

func resultSummary(noun string, entries map[string]any) string {
	names := slices.Sorted(maps.Keys(entries))
	return fmt.Sprintf("%d %s(s): %s", len(names), noun, strings.Join(names, ", "))
}

func chooseCandidates[T any](p *Prompt, question string, cands []T, label func(T) string) []T {
	labels := make([]string, len(cands))
	for i, c := range cands {
		labels[i] = label(c)
	}
	sel := p.MultiChoose(question, labels)
	return candidatesByIndexes(cands, sel)
}

func candidatesByIndexes[T any](cands []T, indexes []int) []T {
	out := make([]T, 0, len(indexes))
	for _, idx := range indexes {
		out = append(out, cands[idx])
	}
	return out
}

func candidateNames[T any](cands []T, name func(T) string) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = name(c)
	}
	return out
}

func detailLabel(title string, details ...string) string {
	parts := append([]string{title}, nonEmpty(details...)...)
	return strings.Join(parts, " · ")
}

func labelField(name, value string) string {
	if value == "" {
		return ""
	}
	return name + ": " + value
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
