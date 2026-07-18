package assist

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/rules"
	volumeinfo "sermo/internal/volume"
)

const (
	volumeConditionFreePct = iota
	volumeConditionUsedPct
	volumeConditionFreeBytes
)

// volumeAssistant creates storage watches: a free/used-space threshold with
// notifications and an optional native auto-expand action.
type volumeAssistant struct{}

const (
	volumeDefaultFreePct        = 10
	volumeDefaultUsedPct        = 90
	volumeDefaultFreeSize       = "10G"
	volumeDefaultUsedSize       = "100G"
	volumeDefaultForCycles      = 3
	volumeDefaultExpandBy       = "5G"
	volumeDefaultExpandCooldown = "30m"
	volumeRootWatchName         = "root"
)

func (volumeAssistant) Name() string { return AssistantNameVolume }
func (volumeAssistant) Title() string {
	return "Storage volume checks (free space, optional auto-expand)"
}

func (volumeAssistant) Run(p *Prompt, env Env) (res Result, err error) {
	// Translate an input-closed re-prompt abort into ErrInputClosed even when
	// Run is driven directly (the CLI also recovers at its own boundary).
	defer Recover(&err)
	vols, err := env.Volumes()
	if err != nil {
		return Result{}, fmt.Errorf("list volumes: %w", err)
	}
	vols = storageVolumeCandidates(vols)
	if len(vols) == 0 {
		return Result{}, errors.New("no storage volumes found to monitor")
	}
	selected, shared := chooseSharedSettings(p,
		"Which volumes do you want to monitor?", vols, volumeLabel,
		"Apply the same settings to all selected volumes?", "the selected volumes",
		func(label string) volSettings { return askVolSettings(p, env, label) })

	watches := map[string]any{}
	forEachWithSettings(selected, shared,
		func(v Volume) volSettings { return askVolSettings(p, env, v.Mountpoint) },
		func(v Volume, s volSettings) {
			watches[watchName(config.WatchCategoryStorage, v.Mountpoint)] = buildVolWatch(v, s)
		})
	return Result{Watches: watches, Summary: fmt.Sprintf("%d storage watch(es)", len(watches))}, nil
}

func storageVolumeCandidates(vols []Volume) []Volume {
	out := make([]Volume, 0, len(vols))
	for _, v := range vols {
		if volumeinfo.IsStorageMount(volumeinfo.Mount{
			Device:     v.Device,
			Mountpoint: v.Mountpoint,
			FSType:     v.FSType,
		}) {
			out = append(out, v)
		}
	}
	return out
}

func volumeLabel(v Volume) string {
	return fmt.Sprintf("%s (%s, %s)", v.Mountpoint, v.FSType, v.Device)
}

// volSettings are the answers gathered for one (or all) volume(s).
type volSettings struct {
	Monitoring        // shared monitor-state + interval (asked first, see docs/wizards.md)
	metric     string // checks.LevelFieldFreePct/UsedPct/FreeBytes/UsedBytes
	op         string
	value      any
	forCycles  int
	notifiers  []string
	dryRun     bool
	expand     bool
	expandBy   string
	cooldown   string
}

func askVolSettings(p *Prompt, env Env, label string) volSettings {
	var s volSettings
	s.Monitoring = p.AskMonitoring(label)
	switch p.Choose("Alert on which condition for "+label+"?", []string{
		"free space below a %",
		"used space at/above a %",
		"free space below a size (K/M/G/T)",
		"used space at/above a size (K/M/G/T)",
	}) {
	case volumeConditionFreePct:
		s.metric, s.op = checks.LevelFieldFreePct, cfgval.CompareOpLess
		s.value = askPercent(p, "Alert when free space drops below", volumeDefaultFreePct)
	case volumeConditionUsedPct:
		s.metric, s.op = checks.LevelFieldUsedPct, cfgval.CompareOpGreaterEqual
		s.value = askPercent(p, "Alert when used space reaches/exceeds", volumeDefaultUsedPct)
	case volumeConditionFreeBytes:
		s.metric, s.op = checks.LevelFieldFreeBytes, cfgval.CompareOpLess
		s.value = askSize(p, "Alert when free space drops below (e.g. 10G)", volumeDefaultFreeSize)
	default:
		s.metric, s.op = checks.LevelFieldUsedBytes, cfgval.CompareOpGreaterEqual
		s.value = askSize(p, "Alert when used space reaches/exceeds (e.g. 100G)", volumeDefaultUsedSize)
	}
	s.forCycles = p.AskInt("Require the condition for how many cycles first?", volumeDefaultForCycles)
	s.notifiers = chooseNotifiers(p, env)
	if p.Confirm("Auto-expand this volume when low? (requires an LVM volume)", false) {
		s.expand = true
		s.expandBy = askSize(p, "Grow by how much each time (e.g. 5G)", volumeDefaultExpandBy)
		s.cooldown = askDuration(p, "Minimum time between expansions (cooldown)", volumeDefaultExpandCooldown)
	}
	s.dryRun = p.AskWatchDryRun(label, env, s.notifiers, s.expand)
	return s
}

func buildVolWatch(v Volume, s volSettings) map[string]any {
	check := map[string]any{
		checks.CheckKeyType: checks.CheckTypeStorage,
		checks.CheckKeyPath: v.Mountpoint,
		s.metric: map[string]any{
			checks.CheckKeyOp:    s.op,
			checks.CheckKeyValue: s.value,
		},
	}
	then := watchThen(s.notifiers)
	if s.expand {
		then[config.WatchThenKeyExpand] = map[string]any{config.WatchExpandKeyBy: s.expandBy}
	}
	entry := map[string]any{
		config.EntryKeyCategory: config.WatchCategoryStorage,
		config.WatchKeyCheck:    check,
		config.WatchKeyThen:     then,
	}
	if s.forCycles > 0 {
		entry[rules.RuleFieldFor] = map[string]any{rules.WindowKeyCycles: s.forCycles}
	}
	if s.expand && s.cooldown != "" {
		entry[rules.SectionPolicy] = map[string]any{rules.PolicyKeyCooldown: s.cooldown}
	}
	s.Monitoring.apply(entry)
	applyDryRun(entry, s.dryRun)
	return entry
}

// askPercent reads a percentage in 0..100 (the bound config validation
// enforces on *_pct predicates), accepting either "10" or "10%".
func askPercent(p *Prompt, question string, def int) any {
	for {
		v := strings.TrimSpace(p.Ask(question+" (%)", cfgval.String(def)))
		if v == "" {
			return def
		}
		if strings.HasSuffix(v, cfgval.PercentSuffix) {
			if _, ok := cfgval.Percent(v); ok {
				return v
			}
		} else if n, ok := cfgval.Int(v); ok {
			if _, ok := cfgval.Percent(n); ok {
				return n
			}
		}
		p.printf("  use a percentage in %s, like 10 or 10%%\n", cfgval.PercentRange())
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
		s = volumeRootWatchName
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
