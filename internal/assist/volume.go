package assist

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const notifyNone = "none"

// volumeAssistant creates `disk` watches: a free/used-space threshold with
// notifications and an optional native auto-expand action.
type volumeAssistant struct{}

func (volumeAssistant) Name() string  { return "volume" }
func (volumeAssistant) Title() string { return "Disk volume checks (free space, optional auto-expand)" }

func (volumeAssistant) Run(p *Prompt, env Env) (Result, error) {
	vols, err := env.Volumes()
	if err != nil {
		return Result{}, fmt.Errorf("list volumes: %w", err)
	}
	if len(vols) == 0 {
		return Result{}, fmt.Errorf("no disk volumes found to monitor")
	}
	labels := make([]string, len(vols))
	for i, v := range vols {
		labels[i] = fmt.Sprintf("%s (%s, %s)", v.Mountpoint, v.FSType, v.Device)
	}
	sel := p.MultiChoose("Which volumes do you want to monitor?", labels)

	var shared *volSettings
	if len(sel) > 1 && p.Confirm("Apply the same settings to all selected volumes?", true) {
		s, err := askVolSettings(p, env, "the selected volumes")
		if err != nil {
			return Result{}, err
		}
		shared = &s
	}

	watches := map[string]any{}
	for _, i := range sel {
		v := vols[i]
		s := shared
		if s == nil {
			t, err := askVolSettings(p, env, v.Mountpoint)
			if err != nil {
				return Result{}, err
			}
			s = &t
		}
		watches[watchName("disk", v.Mountpoint)] = buildVolWatch(v, *s)
	}
	return Result{Watches: watches, Summary: fmt.Sprintf("%d disk watch(es)", len(watches))}, nil
}

// volSettings are the answers gathered for one (or all) volume(s).
type volSettings struct {
	metric    string // free_pct | used_pct | free_bytes | used_bytes
	op        string
	value     any
	forCycles int
	notifiers []string
	expand    bool
	expandBy  string
	cooldown  string
}

func askVolSettings(p *Prompt, env Env, label string) (volSettings, error) {
	var s volSettings
	switch p.Choose("Alert on which condition for "+label+"?", []string{
		"free space below a %",
		"used space at/above a %",
		"free space below a size (K/M/G/T)",
		"used space at/above a size (K/M/G/T)",
	}) {
	case 0:
		s.metric, s.op = "free_pct", "<"
		s.value = p.AskInt("Alert when free space drops below (%)", 10)
	case 1:
		s.metric, s.op = "used_pct", ">="
		s.value = p.AskInt("Alert when used space reaches/exceeds (%)", 90)
	case 2:
		s.metric, s.op = "free_bytes", "<"
		s.value = askSize(p, "Alert when free space drops below (e.g. 10G)", "10G")
	default:
		s.metric, s.op = "used_bytes", ">="
		s.value = askSize(p, "Alert when used space reaches/exceeds (e.g. 100G)", "100G")
	}
	s.forCycles = p.AskInt("Require the condition for how many cycles first?", 3)
	s.notifiers = chooseNotifiers(p, env)
	if p.Confirm("Auto-expand this volume when low? (requires an LVM volume)", false) {
		s.expand = true
		s.expandBy = askSize(p, "Grow by how much each time (e.g. 5G)", "5G")
		s.cooldown = p.Ask("Minimum time between expansions (cooldown)", "30m")
	}
	if !hasNotifyAction(s.notifiers) && !s.expand {
		return s, fmt.Errorf("a watch needs at least one notifier or auto-expand; none chosen for %s", label)
	}
	return s, nil
}

func buildVolWatch(v Volume, s volSettings) map[string]any {
	check := map[string]any{
		"type": "disk",
		"path": v.Mountpoint,
		s.metric: map[string]any{
			"op":    s.op,
			"value": s.value,
		},
	}
	then := map[string]any{}
	if len(s.notifiers) > 0 {
		then["notify"] = s.notifiers
	}
	if s.expand {
		then["expand"] = map[string]any{"by": s.expandBy}
	}
	entry := map[string]any{"check": check, "then": then}
	if s.forCycles > 0 {
		entry["for"] = map[string]any{"cycles": s.forCycles}
	}
	if s.expand && s.cooldown != "" {
		entry["policy"] = map[string]any{"cooldown": s.cooldown}
	}
	return entry
}

// chooseNotifiers asks which configured notifiers to alert. Selecting "none"
// writes the reserved notify sentinel so the generated watch does not inherit a
// global notify default. With no configured notifiers it returns nil without
// prompting.
func chooseNotifiers(p *Prompt, env Env) []string {
	if len(env.Notifiers) == 0 {
		p.printf("  (no notifiers are configured; alerts will rely on the action below)\n")
		return nil
	}
	options := make([]string, 0, len(env.Notifiers)+1)
	options = append(options, "none (do not notify)")
	options = append(options, env.Notifiers...)
	idx := p.MultiChoose("Notify which targets?", options)
	if slices.Contains(idx, 0) {
		if len(idx) == len(options) {
			idx = idx[1:]
		} else {
			return []string{notifyNone}
		}
	}
	if len(idx) == 0 {
		return []string{notifyNone}
	}
	out := make([]string, 0, len(idx))
	for _, i := range idx {
		out = append(out, env.Notifiers[i-1])
	}
	return out
}

func hasNotifyAction(names []string) bool {
	return len(names) > 0 && !slices.Contains(names, notifyNone)
}

// askSize reads a size like 5G, re-prompting on an obviously bad value.
func askSize(p *Prompt, question, def string) string {
	for {
		v := p.Ask(question, def)
		if validSize(v) {
			return v
		}
		p.printf("  use a size like 5G, 500M or 2T\n")
	}
}

// validSize reports whether s looks like a byte size (digits with an optional
// K/M/G/T suffix); the runtime does the authoritative parse.
func validSize(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	last := s[len(s)-1]
	if last < '0' || last > '9' {
		switch last {
		case 'k', 'K', 'm', 'M', 'g', 'G', 't', 'T', 'b', 'B':
			s = s[:len(s)-1]
		default:
			return false
		}
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return err == nil && f > 0
}

// watchName derives a stable watch name from a mount path, e.g. "/mnt/backup"
// -> "disk-mnt-backup", "/" -> "disk-root".
func watchName(prefix, path string) string {
	s := strings.Trim(path, "/")
	if s == "" {
		s = "root"
	}
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '-'
		}
	}, s)
	return prefix + "-" + s
}
