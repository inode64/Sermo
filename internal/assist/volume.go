package assist

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/config"
)

// volumeAssistant creates `storage` watches: a free/used-space threshold with
// notifications and an optional native auto-expand action.
type volumeAssistant struct{}

func (volumeAssistant) Name() string { return "volume" }
func (volumeAssistant) Title() string {
	return "Storage volume checks (free space, optional auto-expand)"
}

func (volumeAssistant) Run(p *Prompt, env Env) (Result, error) {
	vols, err := env.Volumes()
	if err != nil {
		return Result{}, fmt.Errorf("list volumes: %w", err)
	}
	if len(vols) == 0 {
		return Result{}, fmt.Errorf("no storage volumes found to monitor")
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
		watches[watchName("storage", v.Mountpoint)] = buildVolWatch(v, *s)
	}
	return Result{Watches: watches, Summary: fmt.Sprintf("%d storage watch(es)", len(watches))}, nil
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
		s.value = askPercent(p, "Alert when free space drops below", 10)
	case 1:
		s.metric, s.op = "used_pct", ">="
		s.value = askPercent(p, "Alert when used space reaches/exceeds", 90)
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
		s.cooldown = askDuration(p, "Minimum time between expansions (cooldown)", "30m")
	}
	if !config.HasEffectiveNotifyAction(s.notifiers, env.DefaultNotify) && !s.expand {
		return s, fmt.Errorf("a watch needs at least one notifier or auto-expand; none chosen for %s", label)
	}
	return s, nil
}

func buildVolWatch(v Volume, s volSettings) map[string]any {
	check := map[string]any{
		"type": "storage",
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

// chooseNotifiers asks which configured notifiers to alert. The menu always
// offers the same all/none/default vocabulary used across the wizard's
// selections: "all" (the MultiChoose keyword) selects every configured notifier;
// "none" writes the reserved sentinel so the generated watch suppresses any
// inherited default; "default" leaves notify unset (returns nil) so runtime
// inherits the global notify default. The reserved "none" and "default" choices
// are always offered — even when the config defines no notifiers — so an
// expand-only watch (or one that must explicitly opt out) still has a valid
// pick. Each can be entered by number or by typing its name.
func chooseNotifiers(p *Prompt, env Env) []string {
	hasDefault := len(env.DefaultNotify) > 0
	options := make([]string, 0, len(env.Notifiers)+2)
	options = append(options, "none (do not notify)")
	options = append(options, env.Notifiers...)
	defaultIndex := len(options)
	if hasDefault {
		options = append(options, "default (inherit global notify: "+strings.Join(env.DefaultNotify, ", ")+")")
	} else {
		options = append(options, "default (inherit global notify; not configured)")
	}
	idx := p.MultiChoose("Notify which targets?", options)
	if slices.Contains(idx, 0) {
		if len(idx) == len(options) {
			idx = idx[1:]
		} else {
			return []string{config.NotifyNone}
		}
	}
	out := make([]string, 0, len(idx))
	inheritDefault := false
	for _, i := range idx {
		if i == defaultIndex {
			inheritDefault = true
			continue
		}
		out = append(out, env.Notifiers[i-1])
	}
	if len(out) == 0 && inheritDefault {
		return nil
	}
	if len(out) == 0 {
		return []string{config.NotifyNone}
	}
	return out
}

// askPercent reads a percentage in 0..100 (the bound config validation
// enforces on *_pct predicates), accepting either "10" or "10%".
func askPercent(p *Prompt, question string, def int) any {
	for {
		v := strings.TrimSpace(p.Ask(question+" (%)", strconv.Itoa(def)))
		if v == "" {
			return def
		}
		if strings.HasSuffix(v, "%") {
			n := strings.TrimSpace(strings.TrimSuffix(v, "%"))
			if f, err := strconv.ParseFloat(n, 64); err == nil && f >= 0 && f <= 100 {
				return v
			}
		} else if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
			return n
		}
		p.printf("  use a percentage in 0..100, like 10 or 10%%\n")
	}
}

// askDuration reads a positive duration (e.g. 30m), re-prompting on a value
// config validation would reject.
func askDuration(p *Prompt, question, def string) string {
	for {
		v := strings.TrimSpace(p.Ask(question, def))
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return v
		}
		p.printf("  use a positive duration like 30m or 1h\n")
	}
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

// validSize reports whether s is a byte size with an explicit suffix (K/M/G/T,
// with optional B/iB). The runtime does the authoritative parse.
func validSize(s string) bool {
	n, ok := cfgval.ByteSize(s)
	return ok && n > 0
}

// watchName derives a stable watch name from a mount path, e.g. "/mnt/backup"
// -> "storage-mnt-backup", "/" -> "storage-root".
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
