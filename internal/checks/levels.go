package checks

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/metrics"
	"sermo/internal/severity"
)

// A check's `levels:` block grades one measurement on several thresholds:
//
//	used_pct: { op: ">=", value: "80%" }   # fires, graded by `severity:`
//	severity: warning
//	levels:
//	  error:    { used_pct: { op: ">=", value: "95%" } }
//	  critical: { used_pct: { op: ">=", value: "99%" } }
//
// The base threshold decides whether the check fails; each level restates the
// type's own threshold keys with a stricter value and, when it breaches too,
// raises the failure's severity. Levels read the sample the check already took,
// so grading never probes twice. Internally a tier is a "grade" — "level" is
// already the name of the level-predicate grammar the thresholds share.

// gradeKind names how a check type's levels restate its thresholds.
type gradeKind int

const (
	// gradePredicates restates named {op, value} level predicates. Every
	// predicate of a level must hold (AND), except SMART, whose indicators are
	// independent and OR.
	gradePredicates gradeKind = iota + 1
	// gradeMetric restates a metric check's top-level op + value.
	gradeMetric
	// gradeOutput restates a command's expect_stdout {op, value} assertion. The
	// assertion states the passing side, so a level loosens its bound and
	// breaches when that looser assertion fails as well.
	gradeOutput
	// gradeCertDays restates a certificate expiry window in days; a level
	// breaches when fewer days than its window remain.
	gradeCertDays
)

// gradeSupport is one check type's `levels:` capability.
type gradeSupport struct {
	kind gradeKind
	// keys lists the threshold keys a level may restate for this entry's mode;
	// nil means the type's predicate fields. An empty result means this mode
	// has no gradable threshold.
	keys func(entry map[string]any) []string
	// floor is the grade a breach carries when nothing declares one: the
	// type's self-grade (a SMART predicate or an expiring certificate is a
	// warning), or Error. A level must rise above it.
	floor severity.Level
	// defaulted marks a type that fires on a built-in condition when the base
	// declares no threshold (oom fires on any kill), so its levels may add a
	// threshold the base leaves implicit.
	defaulted bool
	// implicit is that built-in condition when it is itself a threshold: a
	// level must tighten it as it would a declared base.
	implicit []levelThreshold
	// anyOf ORs the predicates of one level (SMART).
	anyOf bool
}

// anyCount is the implicit "> 0" a defaulted type fires on.
func anyCount(key string) []levelThreshold {
	return []levelThreshold{{key: key, op: ">", value: 0}}
}

func fixedKeys(keys ...string) func(map[string]any) []string {
	return func(map[string]any) []string { return keys }
}

// gradeSupports lists every type whose thresholds can be graded. A type absent
// here rejects `levels:`: latency probes, composite health verdicts (lvm,
// storcli, ssacli) and state/change metrics have no single ordered threshold.
var gradeSupports = map[string]gradeSupport{
	CheckTypeStorage:          {kind: gradePredicates},
	CheckTypeMemory:           {kind: gradePredicates},
	CheckTypeLoad:             {kind: gradePredicates},
	CheckTypePressure:         {kind: gradePredicates},
	CheckTypeFDS:              {kind: gradePredicates},
	CheckTypePIDs:             {kind: gradePredicates},
	CheckTypeConntrack:        {kind: gradePredicates},
	CheckTypeInotify:          {kind: gradePredicates},
	CheckTypeDiskIO:           {kind: gradePredicates},
	CheckTypeSensors:          {kind: gradePredicates},
	CheckTypeHdparm:           {kind: gradePredicates},
	CheckTypeUsers:            {kind: gradePredicates},
	CheckTypeTCPConnections:   {kind: gradePredicates},
	CheckTypeSSHIdle:          {kind: gradePredicates},
	CheckTypeTerminalSessions: {kind: gradePredicates},
	CheckTypeProcessCount:     {kind: gradePredicates},
	CheckTypeZombies:          {kind: gradePredicates},
	CheckTypeFailedUnits:      {kind: gradePredicates, defaulted: true, implicit: anyCount(CheckKeyCount)},
	CheckTypeOOM:              {kind: gradePredicates, defaulted: true, implicit: anyCount(CheckKeyDelta)},
	CheckTypeEDAC:             {kind: gradePredicates, defaulted: true, implicit: anyCount(fieldUE)},
	CheckTypeRAID:             {kind: gradePredicates, defaulted: true, implicit: anyCount(fieldDegraded)},
	CheckTypeSmart:            {kind: gradePredicates, defaulted: true, anyOf: true, floor: severity.Warning},
	CheckTypeLog:              {kind: gradePredicates, keys: fixedKeys(CheckKeyCount)},
	CheckTypeCount:            {kind: gradePredicates, keys: countGradeKeys},
	CheckTypeSwap:             {kind: gradePredicates, keys: swapGradeKeys, floor: severity.Warning},
	CheckTypeNet:              {kind: gradePredicates, keys: netGradeKeys},
	CheckTypeICMP:             {kind: gradePredicates, keys: icmpGradeKeys},
	CheckTypeMetric:           {kind: gradeMetric, keys: fixedKeys(CheckKeyOp, CheckKeyValue)},
	CheckTypeCommand:          {kind: gradeOutput, keys: fixedKeys(CheckKeyExpectStdout)},
	CheckTypeCert:             {kind: gradeCertDays, keys: fixedKeys(CheckKeyExpiresInDays), floor: severity.Warning},
	CheckTypeHTTP:             {kind: gradeCertDays, keys: fixedKeys(CheckKeyCertExpiresInDays), floor: severity.Warning},
}

func countGradeKeys(entry map[string]any) []string {
	if _, delta := entry[CheckKeyDelta]; delta {
		return []string{CheckKeyDelta}
	}
	return []string{CheckKeyCount}
}

func swapGradeKeys(entry map[string]any) []string {
	switch cfgval.AsString(entry[CheckKeyMetric]) {
	case SwapMetricUsage:
		return SwapUsageFields
	case SwapMetricIO:
		return []string{CheckKeyDelta}
	}
	return nil
}

func netGradeKeys(entry map[string]any) []string {
	if cfgval.AsString(entry[CheckKeyMetric]) == NetMetricErrors {
		return []string{CheckKeyDelta}
	}
	return nil
}

func icmpGradeKeys(entry map[string]any) []string {
	if _, threshold := entry[CheckKeyThreshold]; threshold && cfgval.AsString(entry[CheckKeyMetric]) == IcmpMetricLatency {
		return []string{CheckKeyThreshold}
	}
	return nil
}

// levelFloor is the grade a breach carries when nothing declares one; a level
// must rise above it.
func (g gradeSupport) levelFloor() severity.Level {
	if g.floor.Valid() {
		return g.floor
	}
	return severity.Error
}

func (g gradeSupport) levelKeys(typ string, entry map[string]any) []string {
	if g.keys != nil {
		return g.keys(entry)
	}
	return checkSpecByName[typ].predicateFields
}

// levelSpec is one declared tier: the severity it raises a failure to and the
// threshold keys it restates.
type levelSpec struct {
	level severity.Level
	entry map[string]any
}

// levelThreshold is one ordered threshold, normalised so tiers of every kind
// compare the same way.
type levelThreshold struct {
	key   string
	op    string
	value float64
	// percent marks a metric threshold in its percentage form; a level must use
	// the same form as the threshold it tightens.
	percent bool
}

// bound renders the threshold's value the way the operator wrote it, keeping
// a metric threshold's percentage form.
func (t levelThreshold) bound() string {
	if t.percent {
		return formatThreshold(t.value) + "%"
	}
	return formatThreshold(t.value)
}

// LevelCheck is the outcome of checking one entry's `levels:` block.
type LevelCheck struct {
	// Errors are shape errors that make the configuration invalid.
	Errors []string
	// Ignored describes levels that are well-formed but inert — not stricter
	// than the threshold below them, or not above the check's own severity — so
	// the check keeps them out of its grading instead of refusing to load.
	Ignored []string
	kept    []levelSpec
}

// ValidateLevels checks entry's `levels:` block for typ against the
// narrowest declared severity (unset when nothing declares one). Messages are
// relative to the entry and start with "levels". It is the same routine
// construction uses, so a level the validator accepts is the level the check
// grades.
func ValidateLevels(typ string, entry map[string]any, declared severity.Level) LevelCheck {
	raw, present := entry[CheckKeyLevels]
	if !present {
		return LevelCheck{}
	}
	if disabled, isBool := raw.(bool); isBool && !disabled {
		return LevelCheck{}
	}
	block, ok := raw.(map[string]any)
	if !ok {
		return LevelCheck{Errors: []string{CheckKeyLevels + " must be a mapping of severity: thresholds, or false"}}
	}
	if len(block) == 0 {
		return LevelCheck{Errors: []string{CheckKeyLevels + " must declare at least one level"}}
	}
	if reports := cfgval.AsString(entry[CheckKeyReports]); reports != "" {
		// Another reports: inverts or drops the condition verdict the tiers
		// grade, so they are inert — typically tiers a catalog check brought
		// to an override that turned it into a sensor. Refusing the load over
		// them would punish the override.
		if info, known := TypeInfoFor(typ); known && reports != info.DefaultReports {
			return LevelCheck{Ignored: []string{fmt.Sprintf("%s grade the default %s: %s, not %s; ignored", CheckKeyLevels, CheckKeyReports, info.DefaultReports, reports)}}
		}
	}
	support, supported := gradeSupports[typ]
	if !supported {
		return LevelCheck{Errors: []string{fmt.Sprintf("%s are not supported on a %s check", CheckKeyLevels, typ)}}
	}
	keys := support.levelKeys(typ, entry)
	if len(keys) == 0 {
		return LevelCheck{Errors: []string{fmt.Sprintf("%s are not supported for this %s metric: it has no ordered threshold to grade", CheckKeyLevels, typ)}}
	}
	var out LevelCheck
	base, err := support.thresholds(keys, entry, true)
	if err != nil {
		out.Errors = append(out.Errors, CheckKeyLevels+": "+err.Error())
	} else if len(base) == 0 && !support.defaulted {
		out.Errors = append(out.Errors, fmt.Sprintf("%s need a base threshold (%s) to escalate from", CheckKeyLevels, strings.Join(keys, "/")))
	}
	specs := out.parseLevels(support, keys, block)
	if len(out.Errors) > 0 {
		return LevelCheck{Errors: out.Errors}
	}
	floor := declared
	if !floor.Valid() {
		floor = support.levelFloor()
	}
	out.keep(support, keys, base, floor, specs)
	return out
}

// parseLevels reads each named tier, ascending, dropping `false` tiers.
func (out *LevelCheck) parseLevels(support gradeSupport, keys []string, block map[string]any) []levelSpec {
	var specs []levelSpec
	for _, name := range slices.Sorted(maps.Keys(block)) {
		path := CheckKeyLevels + "." + name
		level, ok := severity.Parse(name)
		if !ok {
			out.Errors = append(out.Errors, fmt.Sprintf("%s is not a severity (%s)", path, severity.Summary))
			continue
		}
		if disabled, isBool := block[name].(bool); isBool && !disabled {
			continue
		}
		tier, ok := block[name].(map[string]any)
		if !ok {
			out.Errors = append(out.Errors, path+" must be a mapping of thresholds, or false")
			continue
		}
		if len(tier) == 0 {
			out.Errors = append(out.Errors, fmt.Sprintf("%s must restate at least one of %s", path, strings.Join(keys, "/")))
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(tier)) {
			if !slices.Contains(keys, key) {
				out.Errors = append(out.Errors, fmt.Sprintf("%s.%s is not a threshold of this check (%s)", path, key, strings.Join(keys, "/")))
			}
		}
		if _, err := support.thresholds(keys, tier, false); err != nil {
			out.Errors = append(out.Errors, path+": "+err.Error())
		}
		specs = append(specs, levelSpec{level: level, entry: tier})
	}
	slices.SortFunc(specs, func(a, b levelSpec) int { return a.level.Rank() - b.level.Rank() })
	return specs
}

// keep admits each tier that rises above floor and tightens every threshold it
// shares with the base or with the last admitted tier; the rest are ignored.
func (out *LevelCheck) keep(support gradeSupport, keys []string, base []levelThreshold, floor severity.Level, specs []levelSpec) {
	below := map[string]levelThreshold{}
	if len(base) == 0 {
		base = support.implicit
	}
	for _, t := range base {
		below[t.key] = t
	}
	for _, spec := range specs {
		path := CheckKeyLevels + "." + spec.level.String()
		if spec.level.Rank() <= floor.Rank() {
			out.Ignored = append(out.Ignored, fmt.Sprintf("%s is not above the check's severity %s; ignored", path, floor))
			continue
		}
		tier, _ := support.thresholds(keys, spec.entry, false)
		reason := ""
		for _, t := range tier {
			if prev, shared := below[t.key]; shared {
				if reason = looserThan(support.kind, prev, t); reason != "" {
					reason = fmt.Sprintf("%s.%s %s", path, t.key, reason)
					break
				}
			}
		}
		if reason != "" {
			out.Ignored = append(out.Ignored, reason+"; ignored")
			continue
		}
		for _, t := range tier {
			below[t.key] = t
		}
		out.kept = append(out.kept, spec)
	}
}

// thresholdDirection is +1 when a larger value is the stricter alarm, -1 when
// a smaller one is, and 0 for an unordered comparison.
func thresholdDirection(kind gradeKind, op string) int {
	direction := 0
	switch op {
	case cfgval.CompareOpGreater, cfgval.CompareOpGreaterEqual:
		direction = 1
	case cfgval.CompareOpLess, cfgval.CompareOpLessEqual:
		direction = -1
	}
	if kind == gradeOutput {
		// The assertion states the passing side: `<= 200` alarms above 200, so
		// the stricter alarm is the larger bound.
		direction = -direction
	}
	return direction
}

// looserThan explains why next does not tighten prev, or returns "".
func looserThan(kind gradeKind, prev, next levelThreshold) string {
	if prev.percent != next.percent {
		return "must use the same form (percentage or absolute) as the threshold below it"
	}
	direction := thresholdDirection(kind, next.op)
	if direction == 0 || thresholdDirection(kind, prev.op) == 0 {
		// Equality has no stricter side: the tier could only breach where the
		// threshold below it already failed to.
		return fmt.Sprintf("compares %s against %s %s: only an ordered comparison (>, >=, <, <=) can tighten it", next.op, prev.op, prev.bound())
	}
	if direction != thresholdDirection(kind, prev.op) {
		return fmt.Sprintf("compares %s, the opposite direction of %s", next.op, prev.op)
	}
	switch {
	case direction > 0 && next.value <= prev.value, direction < 0 && next.value >= prev.value:
		return fmt.Sprintf("%s %s is not stricter than %s %s", next.op, next.bound(), prev.op, prev.bound())
	}
	return ""
}

// thresholds reads the ordered thresholds entry declares among keys. base marks
// the check's own entry, which may spell a threshold in a legacy shape (a count
// check's top-level op/value).
func (g gradeSupport) thresholds(keys []string, entry map[string]any, base bool) ([]levelThreshold, error) {
	switch g.kind {
	case gradeMetric:
		return metricThreshold(entry, base)
	case gradeOutput:
		return outputThreshold(entry, base)
	case gradeCertDays:
		return certDaysThreshold(keys[0], entry)
	case gradePredicates:
		// named level predicates, read below
	}
	var out []levelThreshold
	var errs []error
	for _, key := range keys {
		raw, present := entry[key]
		if !present && base && key == CheckKeyCount {
			if _, top := entry[CheckKeyOp]; top {
				raw, present = entry, true // count check: top-level op/value
			}
		}
		if !present {
			continue
		}
		op, value, err := ParsePredicate(key, raw)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, levelThreshold{key: key, op: op, value: value})
	}
	return out, errors.Join(errs...)
}

func metricThreshold(entry map[string]any, base bool) ([]levelThreshold, error) {
	op := cfgval.AsString(entry[CheckKeyOp])
	raw, hasValue := entry[CheckKeyValue]
	if op == "" && !hasValue && base {
		return nil, nil
	}
	if !cfgval.IsCompareOp(op) {
		return nil, fmt.Errorf("%s %q is not one of %s", CheckKeyOp, op, cfgval.CompareOpSummary)
	}
	value, percent, err := metrics.ParseThreshold(cfgval.String(raw))
	if err != nil {
		return nil, fmt.Errorf("%s %w", CheckKeyValue, err)
	}
	return []levelThreshold{{key: CheckKeyValue, op: op, value: value, percent: percent}}, nil
}

func outputThreshold(entry map[string]any, base bool) ([]levelThreshold, error) {
	raw, present := entry[CheckKeyExpectStdout]
	if !present {
		return nil, nil
	}
	assertion, isMap := raw.(map[string]any)
	if !isMap {
		if base {
			return nil, nil // a substring expectation has no ordered bound
		}
		return nil, fmt.Errorf("%s must be an {op, value} mapping", CheckKeyExpectStdout)
	}
	op := cfgval.String(assertion[CheckKeyOp])
	if thresholdDirection(gradeOutput, op) == 0 {
		if base {
			return nil, nil
		}
		return nil, fmt.Errorf("%s op %q must be an ordered comparison (>, >=, <, <=)", CheckKeyExpectStdout, op)
	}
	value, err := parseFiniteThreshold(assertion[CheckKeyValue])
	if err != nil {
		return nil, fmt.Errorf("%s value %w", CheckKeyExpectStdout, err)
	}
	return []levelThreshold{{key: CheckKeyExpectStdout, op: op, value: value}}, nil
}

func certDaysThreshold(key string, entry map[string]any) ([]levelThreshold, error) {
	raw, present := entry[key]
	if !present {
		return nil, nil
	}
	days, ok := cfgval.Int(raw)
	if !ok || days <= 0 {
		return nil, fmt.Errorf("%s must be a positive number of days", key)
	}
	return []levelThreshold{{key: key, op: cfgval.CompareOpLess, value: float64(days)}}, nil
}

// buildLevels returns the tiers a check grades with, or a build failure for a
// malformed block. Inert tiers are left out silently here: configuration
// validation reports them.
func buildLevels(typ string, entry map[string]any, declared severity.Level) ([]levelSpec, *buildFailure) {
	result := ValidateLevels(typ, entry, declared)
	if len(result.Errors) > 0 {
		return nil, &buildFailure{detail: strings.Join(result.Errors, "; ")}
	}
	return result.kept, nil
}

// grade is one kept tier in the form its check compares: the severity it
// raises a failure to and the threshold it restates.
type grade[T any] struct {
	level     severity.Level
	threshold T
}

// grades are a check's kept tiers. Every gradable type keeps its tiers this
// way, so "the gravest tier that also breaches" is decided in one place.
type grades[T any] []grade[T]

// parseGrades converts kept tiers with parse. A tier parse rejects was already
// reported by ValidateLevels, so it is dropped rather than graded.
func parseGrades[T any](specs []levelSpec, parse func(map[string]any) (T, bool)) grades[T] {
	out := make(grades[T], 0, len(specs))
	for _, spec := range specs {
		if threshold, ok := parse(spec.entry); ok {
			out = append(out, grade[T]{level: spec.level, threshold: threshold})
		}
	}
	return out
}

// highest is the gravest tier whose threshold breach reports as breached, or
// unset when none does.
func (g grades[T]) highest(breach func(T) bool) severity.Level {
	var top severity.Level
	for _, tier := range g {
		if breach(tier.threshold) {
			top = severity.Max(top, tier.level)
		}
	}
	return top
}

// predicateGrades parses the kept tiers of a level-predicate type.
func predicateGrades(typ string, entry map[string]any, specs []levelSpec) grades[[]levelPred] {
	support, ok := gradeSupports[typ]
	if !ok || support.kind != gradePredicates || len(specs) == 0 {
		return nil
	}
	keys := support.levelKeys(typ, entry)
	return parseGrades(specs, func(tier map[string]any) ([]levelPred, bool) {
		preds, err := parseLevelPreds(tier, keys)
		return preds, err == nil && len(preds) > 0
	})
}

// raiseSeverity lifts a failing result to level when level is graver than the
// severity the result already carries. Grading never invents a failure, never
// touches a healthy, unavailable or verdictless result, and never lowers a
// grade.
func raiseSeverity(res Result, level severity.Level) Result {
	if level.Valid() && res.Observation() == ObservationFailing && level.Rank() > res.Severity.Resolved().Rank() {
		res.Severity = level
	}
	return res
}

// grade raises a failing level-predicate result to the highest tier whose
// predicates also hold against values — the readings the check compared its
// base threshold against.
func (b base) grade(res Result, values map[string]float64) Result {
	return raiseSeverity(res, b.grades.highest(func(preds []levelPred) bool {
		if b.gradeAnyOf {
			return len(holdingLevelPreds(preds, values)) > 0
		}
		return levelPredsHold(preds, values)
	}))
}

// gradeValue is grade for a single-threshold check whose reading is one value.
func (b base) gradeValue(res Result, key string, value float64) Result {
	if len(b.grades) == 0 {
		return res
	}
	return b.grade(res, map[string]float64{key: value})
}
