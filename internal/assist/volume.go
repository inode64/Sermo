package assist

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/rules"
	"sermo/internal/severity"
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
	vols, err := detectCandidates(env.Volumes, "volume detection is unavailable", "list volumes")
	if err != nil {
		return Result{}, err
	}
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

func volumeLabel(v Volume) string {
	return fmt.Sprintf("%s (%s, %s)", v.Mountpoint, v.FSType, v.Device)
}

// volSettings are the answers gathered for one (or all) volume(s).
type volSettings struct {
	Monitoring        // shared monitor-state + interval (asked first)
	metric     string // checks.LevelFieldFreePct/UsedPct/FreeBytes/UsedBytes
	op         string
	value      any
	// severity grades the base threshold: warning for a percentage condition
	// (asked as "Warn when"), unset for a size condition.
	severity severity.Level
	// levels grades a percentage condition (warning, then error and
	// critical); nil for a size condition, which keeps one threshold.
	levels    map[string]any
	forCycles int
	notifiers []string
	dryRun    bool
	expand    bool
	expandBy  string
	cooldown  string
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
		s.askGradedPercent(p, percentLadder{checks.LevelFieldFreePct, cfgval.CompareOpLess, "free space drops below",
			storageWarnFreePct, storageErrorFreePct, storageCriticalFreePct})
	case volumeConditionUsedPct:
		s.askGradedPercent(p, percentLadder{checks.LevelFieldUsedPct, cfgval.CompareOpGreaterEqual, "used space reaches/exceeds",
			storageWarnUsedPct, storageErrorUsedPct, storageCriticalUsedPct})
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
		s.cooldown = p.askPositiveDuration(
			"Minimum time between expansions (cooldown)", volumeDefaultExpandCooldown,
			"use a positive duration like 30m or 1h", false,
		)
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
	if s.severity.Valid() {
		check[checks.CheckKeySeverity] = s.severity.String()
	}
	if s.levels != nil {
		check[checks.CheckKeyLevels] = s.levels
	}
	then := watchThen(s.notifiers)
	if s.expand {
		then[config.WatchThenKeyExpand] = map[string]any{config.WatchExpandKeyBy: s.expandBy}
	}
	entry := map[string]any{
		config.WatchKeyCheck: check,
		config.WatchKeyThen:  then,
	}
	if s.forCycles > 0 {
		entry[rules.RuleFieldFor] = map[string]any{rules.WindowKeyCycles: s.forCycles}
	}
	if s.expand && s.cooldown != "" {
		entry[rules.SectionPolicy] = map[string]any{rules.PolicyKeyCooldown: s.cooldown}
	}
	applyWatchSettings(entry, config.WatchCategoryStorage, s.Monitoring, s.dryRun)
	return entry
}

// askPercent reads a percentage in 0..100 (the bound config validation
// enforces on *_pct predicates), accepting either "10" or "10%".
func askPercent(p *Prompt, question string, def int) any {
	for {
		v := p.Ask(question+" (%)", cfgval.String(def))
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

// percentLadder is one graded percentage condition and its default rungs.
type percentLadder struct {
	field, op, condition                       string
	warnDefault, errorDefault, criticalDefault int
}

// askGradedPercent asks a percentage condition's warning threshold and the
// levels it escalates to.
func (s *volSettings) askGradedPercent(p *Prompt, l percentLadder) {
	s.metric, s.op, s.severity = l.field, l.op, severity.Warning
	s.value = askPercent(p, "Warn when "+l.condition, l.warnDefault)
	s.levels = askPercentLevels(p, l, s.value)
}

// askPercentLevels asks where a percentage condition escalates to error and
// to critical, re-prompting until each level is stricter than the one below
// it: Sermo ignores a level that is not. A tier with no stricter percentage
// left (a warning already at 100 % used or 0 % free) is not asked; nil means
// the condition keeps a single threshold.
func askPercentLevels(p *Prompt, l percentLadder, warn any) map[string]any {
	levels := map[string]any{}
	below := warn
	for _, tier := range []struct {
		level severity.Level
		def   int
	}{{severity.Error, l.errorDefault}, {severity.Critical, l.criticalDefault}} {
		if !stricterPercentExists(l.op, below) {
			break
		}
		value := askStricterPercent(p, "Escalate to "+tier.level.String()+" when "+l.condition, l.op, below, tier.def)
		levels[tier.level.String()] = storageLevel(l.field, l.op, value)
		below = value
	}
	if len(levels) == 0 {
		return nil
	}
	return levels
}

// tightens reports whether value is a stricter threshold than floor: smaller
// for a "<" condition, larger for ">=".
func tightens(op string, value, floor float64) bool {
	if op == cfgval.CompareOpLess {
		return value < floor
	}
	return value > floor
}

// stricterPercentExists reports whether any valid percentage tightens below.
func stricterPercentExists(op string, below any) bool {
	floor, _ := cfgval.Percent(below)
	direction := math.Inf(1)
	if op == cfgval.CompareOpLess {
		direction = math.Inf(-1)
	}
	_, ok := cfgval.Percent(math.Nextafter(floor, direction))
	return ok
}

// askStricterPercent reads a percentage that tightens below. The caller has
// checked that one exists, and closed input aborts the re-prompt loop.
func askStricterPercent(p *Prompt, question, op string, below any, def int) any {
	floor, _ := cfgval.Percent(below)
	def = stricterDefault(op, floor, def)
	for {
		v := askPercent(p, question, def)
		if value, _ := cfgval.Percent(v); tightens(op, value, floor) {
			return v
		}
		p.abortIfClosed()
		p.printf("  must be stricter than %s%%\n", cfgval.String(floor))
	}
}

// stricterDefault keeps a tier's offered default valid: the ladder's own
// default when it tightens floor, otherwise the nearest whole percentage that
// does (a warning already past the default ladder). The caller has checked
// that a stricter percentage exists, so floor is inside 0..100 exclusive on
// the tightening side and the result stays in range.
func stricterDefault(op string, floor float64, def int) int {
	if tightens(op, float64(def), floor) {
		return def
	}
	if op == cfgval.CompareOpLess {
		return int(math.Ceil(floor)) - 1
	}
	return int(math.Floor(floor)) + 1
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
