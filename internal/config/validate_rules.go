package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/emission"
	"sermo/internal/metrics"
	"sermo/internal/process"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
)

// validateWindow checks an optional for/within firing window at the dotted prefix,
// shared by rules, host watches and per-metric sub-watches. A window may declare
// at most one of for/within; each window must choose exactly one of cycles or
// duration; cycles and duration must be positive; and within.min_matches —
// optional, defaulting to 1 (true at least once within the window) — must be
// positive and no larger than within.cycles when cycles are used.
func validateWindow(prefix string, entry map[string]any, add addFunc) {
	rawFor, hasFor := entry[rules.RuleFieldFor]
	rawWithin, hasWithin := entry[rules.RuleFieldWithin]
	if hasFor && hasWithin {
		add("%s cannot define both for and within", prefix)
	}
	if hasFor {
		f, ok := rawFor.(map[string]any)
		if !ok {
			// A scalar (`for: 3`) would otherwise be silently ignored by the
			// runtime parser, leaving the rule without a window.
			add("%s.for must be a mapping, e.g. for: {cycles: 3} or for: {duration: 6m}", prefix)
		} else {
			for _, key := range slices.Sorted(maps.Keys(f)) {
				if key != rules.WindowKeyCycles && key != rules.WindowKeyDuration {
					add("%s.for.%s is not supported; for is always consecutive and only accepts cycles or duration", prefix, key)
				}
			}
			validateWindowLength(prefix+".for", f, add)
		}
	}
	if hasWithin {
		wn, ok := rawWithin.(map[string]any)
		if !ok {
			add("%s.within must be a mapping, e.g. within: {cycles: 5, min_matches: 2} or within: {duration: 30m, min_matches: 2}", prefix)
			return
		}
		for _, key := range slices.Sorted(maps.Keys(wn)) {
			if key != rules.WindowKeyCycles && key != rules.WindowKeyDuration && key != rules.WindowKeyMinMatches {
				add("%s.within.%s is not supported; within only accepts cycles or duration, plus min_matches", prefix, key)
			}
		}
		cycles, hasCycles := validateWindowLength(prefix+".within", wn, add)
		if v, present := wn[rules.WindowKeyMinMatches]; present {
			matches, _ := cfgval.Int(v)
			switch {
			case matches <= 0:
				add("%s.within.min_matches must be > 0", prefix)
			case hasCycles && cycles > 0 && matches > cycles:
				add("%s.within.min_matches must be <= within.cycles", prefix)
			}
		}
	}
}

func validateWindowLength(prefix string, m map[string]any, add addFunc) (cycles int, hasCycles bool) {
	_, hasCycles = m[rules.WindowKeyCycles]
	_, hasDuration := m[rules.WindowKeyDuration]
	switch {
	case !hasCycles && !hasDuration:
		add("%s must define exactly one of cycles or duration", prefix)
	case hasCycles && hasDuration:
		add("%s cannot define both cycles and duration", prefix)
	}
	if hasCycles {
		cycles, _ = cfgval.Int(m[rules.WindowKeyCycles])
		if cycles <= 0 {
			add("%s.cycles must be > 0", prefix)
		}
	}
	if hasDuration && !isPositiveDuration(cfgval.String(m[rules.WindowKeyDuration])) {
		add("%s.duration must be a valid positive duration", prefix)
	}
	return cycles, hasCycles
}

var serviceStates = set(
	string(servicemgr.StatusActive),
	string(servicemgr.StatusInactive),
	string(servicemgr.StatusPaused),
	string(servicemgr.StatusFailed),
	string(servicemgr.StatusUnknown),
)
var processStates = set(process.StateRunning, process.StateZombie, process.StateAbsent)
var validActions = set(
	string(rules.ActionRestart),
	string(rules.ActionStart),
	string(rules.ActionStop),
	string(rules.ActionReload),
	string(rules.ActionResume),
	string(rules.ActionAlert),
	string(rules.ActionBlock),
)
var metricCatalog = map[string]map[string]struct{}{
	checks.MetricScopeService: set(metrics.MetricMemory, metrics.MetricSwap, metrics.MetricCPU, metrics.MetricCPUThread,
		metrics.MetricProcessCount, metrics.MetricIO, metrics.MetricIORead, metrics.MetricIOWrite,
		metrics.MetricFds, metrics.MetricThreads),
	checks.MetricScopeSystem: set(metrics.MetricTotalMemory, metrics.MetricTotalSwap, metrics.MetricTotalCPU,
		metrics.MetricLoad1, metrics.MetricLoad5, metrics.MetricLoad15),
}

// metricForms records which value forms each metric exposes, so
// a threshold's form can be checked against the metric.
type metricForm struct{ absolute, percent bool }

var metricForms = map[string]metricForm{
	metrics.MetricMemory:       {absolute: true, percent: true},
	metrics.MetricSwap:         {absolute: true, percent: true},
	metrics.MetricCPU:          {percent: true},
	metrics.MetricCPUThread:    {percent: true},
	metrics.MetricProcessCount: {absolute: true},
	metrics.MetricIO:           {absolute: true},
	metrics.MetricIORead:       {absolute: true},
	metrics.MetricIOWrite:      {absolute: true},
	metrics.MetricFds:          {absolute: true},
	metrics.MetricThreads:      {absolute: true},
	metrics.MetricTotalMemory:  {absolute: true, percent: true},
	metrics.MetricTotalSwap:    {absolute: true, percent: true},
	metrics.MetricTotalCPU:     {percent: true},
	metrics.MetricLoad1:        {absolute: true},
	metrics.MetricLoad5:        {absolute: true},
	metrics.MetricLoad15:       {absolute: true},
}

// validateRuleWindow checks the merged `rule_window` fallback block: a positive
// cycles or duration window, a known mode, and — for the within mode — an
// optional min_matches (default 1) that is positive and no larger than cycles
// when cycles are used.
func validateRuleWindow(tree map[string]any, add addFunc) {
	rw, present := tree[sectionRuleWindow]
	if !present {
		return
	}
	m, ok := rw.(map[string]any)
	if !ok {
		add("rule_window must be a mapping")
		return
	}
	cycles, hasCycles := validateWindowLength(sectionRuleWindow, m, add)
	switch mode := cfgval.String(m[rules.FieldMode]); mode {
	case "", rules.WindowModeConsecutive:
	case rules.WindowModeWithin:
		if v, present := m[rules.WindowKeyMinMatches]; present {
			matches, _ := cfgval.Int(v)
			switch {
			case matches <= 0:
				add("rule_window.min_matches must be > 0 for mode %q", mode)
			case hasCycles && cycles > 0 && matches > cycles:
				add("rule_window.min_matches must be <= rule_window.cycles")
			}
		}
	default:
		add("rule_window.mode %q is not one of %s", mode, rules.WindowModeSummary)
	}
}

func validateRules(tree map[string]any, notifiers map[string]struct{}, add addFunc) {
	ruleMap, ok := tree[rules.SectionRules].(map[string]any)
	if !ok {
		return
	}
	checkNames := collectCheckNames(tree)
	systemMetricChecks := collectSystemMetricChecks(tree)

	for _, name := range slices.Sorted(maps.Keys(ruleMap)) {
		path := rules.SectionRules + "." + name
		entry, ok := ruleMap[name].(map[string]any)
		if !ok {
			add(validationMappingFormat, path)
			continue
		}
		validateRule(path, entry, notifiers, checkNames, systemMetricChecks, add)
	}
}

func validateRule(path string, entry map[string]any, notifiers, checkNames, systemMetricChecks map[string]struct{}, add addFunc) {
	if _, present := entry[rules.RuleFieldNotify]; present {
		validateNotifySelection(path+".notify", entry[rules.RuleFieldNotify], notifiers, add)
	}
	validateEmission(entry, path+"."+emission.Section, add)
	ruleType := validateRuleType(path, entry, add)
	ifNode, hasIf := entry[rules.RuleFieldIf].(map[string]any)
	if !hasIf {
		add("%s has no if condition", path)
	}
	then, hasThen := entry[rules.RuleFieldThen].(map[string]any)
	if !hasThen {
		add("%s has no then action", path)
	}
	actions := ruleActions(then)
	validateRuleActions(path, entry, ruleType, actions, hasThen, add)
	validateWindow(path, entry, add)
	if hasIf {
		validateCondition(ifNode, path+".if", checkNames, systemMetricChecks, ruleType == string(rules.RuleAlert), add)
	}
}

func validateRuleType(path string, entry map[string]any, add addFunc) string {
	ruleType := cfgval.String(entry[rules.RuleFieldType])
	switch ruleType {
	case string(rules.RuleRemediation), string(rules.RuleGuard), string(rules.RuleAlert):
	default:
		add("%s type %q is not one of %s", path, ruleType, rules.RuleTypeSummary)
	}
	return ruleType
}

func validateRuleActions(path string, entry map[string]any, ruleType string, actions []valAction, hasThen bool, add addFunc) {
	isGuard := ruleType == string(rules.RuleGuard)
	blocks, blocksErr := cfgval.StrictStringList(entry[rules.RuleFieldBlocks])
	if _, present := entry[rules.RuleFieldBlocks]; present && blocksErr != nil {
		add(validationStringListFormat, path+"."+rules.RuleFieldBlocks)
	}
	hasBlock := validateRuleActionForms(path, actions, isGuard, add)
	validateRuleGuardActions(path, entry, isGuard, blocks, blocksErr, hasBlock, add)
	hasOperation := validateRuleOperationActions(path, ruleType, actions, add)
	if ruleType == string(rules.RuleRemediation) && hasThen && !hasOperation {
		add("%s remediation requires an operation action (restart, start, stop, reload, resume); use type: alert for notify-only rules", path)
	}
}

func validateRuleActionForms(path string, actions []valAction, isGuard bool, add addFunc) bool {
	hasBlock := false
	for _, action := range actions {
		if action.typ != "" {
			if _, ok := validActions[action.typ]; !ok {
				add("%s then.action %q is not one of %s", path, action.typ, rules.RuleActionSummary)
			}
		}
		if action.typ == string(rules.ActionBlock) {
			hasBlock = true
			if !isGuard {
				add("%s only guard rules may use action block", path)
			}
		}
		if (action.typ == string(rules.ActionBlock) || action.typ == string(rules.ActionAlert)) && action.message == "" {
			add("%s action %s requires a non-empty message", path, action.typ)
		}
	}
	return hasBlock
}

func validateRuleGuardActions(path string, entry map[string]any, isGuard bool, blocks []string, blocksErr error, hasBlock bool, add addFunc) {
	if !isGuard {
		if len(blocks) > 0 {
			add("%s only guard rules may set blocks", path)
		}
		return
	}
	if blocksErr != nil || len(blocks) == 0 {
		add("%s guard requires a non-empty blocks list", path)
	}
	if !hasBlock {
		add("%s guard rules must use action block", path)
	}
	if _, ok := entry[rules.RuleFieldFor]; ok {
		add("%s guard rules do not support a for window", path)
	}
	if _, ok := entry[rules.RuleFieldWithin]; ok {
		add("%s guard rules do not support a within window", path)
	}
}

func validateRuleOperationActions(path, ruleType string, actions []valAction, add addFunc) bool {
	hasOperation := false
	for _, action := range actions {
		switch rules.ActionType(action.typ) {
		case rules.ActionRestart, rules.ActionStart, rules.ActionStop, rules.ActionReload, rules.ActionResume:
			hasOperation = true
			if ruleType != string(rules.RuleRemediation) {
				add("%s only remediation rules may use action %s", path, action.typ)
			}
		}
	}
	return hasOperation
}

var conditionOperators = []string{
	rules.ConditionAnd,
	rules.ConditionOr,
	rules.ConditionNot,
	rules.ConditionFailed,
	rules.ConditionActive,
	rules.ConditionMetric,
	rules.ConditionService,
	rules.ConditionProcess,
	rules.ConditionFile,
	rules.ConditionCommand,
	rules.ConditionChanged,
}

// validateCondition checks one condition node: exactly one operator/leaf, valid
// check references, valid service/process states, command array+timeout, and
// metric grammar (with system-scope allowed only in alert rules).
func validateCondition(node map[string]any, path string, checkNames, systemMetricChecks map[string]struct{}, allowSystemMetric bool, add addFunc) {
	present := presentOperators(node)
	if len(present) != 1 {
		add("%s must contain exactly one condition/operator", path)
		return
	}
	key := present[0]

	switch key {
	case rules.ConditionAnd, rules.ConditionOr:
		validateLogicalCondition(node[key], key, path, checkNames, systemMetricChecks, allowSystemMetric, add)
	case rules.ConditionNot:
		validateNotCondition(node[key], path, checkNames, systemMetricChecks, allowSystemMetric, add)
	case rules.ConditionFailed, rules.ConditionActive:
		validateProbe(node[key], path+"."+key, checkNames, systemMetricChecks, allowSystemMetric, add)
	case rules.ConditionService:
		validateState(node[rules.ConditionService], rules.FieldState, serviceStates, servicemgr.StatusSummary, path+".service", add)
	case rules.ConditionProcess:
		validateState(node[rules.ConditionProcess], rules.FieldState, processStates, process.StateSummary, path+".process", add)
	case rules.ConditionFile:
		validateFileCondition(node[key], path, add)
	case rules.ConditionCommand:
		validateCommandCondition(node[key], path, add)
	case rules.ConditionMetric:
		if m, ok := node[rules.ConditionMetric].(map[string]any); ok {
			validateMetric(m, path+".metric", allowSystemMetric, add)
		}
	case rules.ConditionChanged:
		validateChanged(node[rules.ConditionChanged], path+".changed", treeAppVersionChecks(checkNames), add)
	}
}

func validateLogicalCondition(value any, operator, path string, checkNames, systemMetricChecks map[string]struct{}, allowSystemMetric bool, add addFunc) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		add("%s.%s requires a non-empty list", path, operator)
		return
	}
	for i, item := range items {
		child, ok := item.(map[string]any)
		if !ok {
			add("%s.%s[%d] must be a condition", path, operator, i)
			continue
		}
		validateCondition(child, fmt.Sprintf("%s.%s[%d]", path, operator, i), checkNames, systemMetricChecks, allowSystemMetric, add)
	}
}

func validateNotCondition(value any, path string, checkNames, systemMetricChecks map[string]struct{}, allowSystemMetric bool, add addFunc) {
	child, ok := value.(map[string]any)
	if !ok {
		add("%s.not must be a condition", path)
		return
	}
	validateCondition(child, path+".not", checkNames, systemMetricChecks, allowSystemMetric, add)
}

func validateFileCondition(value any, path string, add addFunc) {
	m, ok := value.(map[string]any)
	if !ok || cfgval.String(m[rules.FieldPath]) == "" {
		add("%s.file requires a path", path)
	}
	if !ok {
		return
	}
	// `exists` defaults to true at runtime; a non-boolean (e.g. the string
	// "false") would silently act as true.
	if v, present := m[rules.FieldExists]; present {
		if _, isBool := v.(bool); !isBool {
			add(validationBooleanFormat, path+"."+rules.ConditionFile+"."+rules.FieldExists)
		}
	}
}

func validateCommandCondition(value any, path string, add addFunc) {
	m, ok := value.(map[string]any)
	if !ok {
		add("%s.command must be a mapping", path)
		return
	}
	entry := maps.Clone(m)
	entry[checks.CheckKeyType] = checks.CheckTypeCommand
	validateSingleShotCheckFields(path+".command", checks.CheckTypeCommand, entry, "", add)
	if cfgval.String(m[checks.CheckKeyTimeout]) == "" {
		add("%s.command condition must declare a timeout", path)
	}
}

func validateChanged(v any, path string, appVersionChecks map[string]struct{}, add addFunc) {
	m, ok := v.(map[string]any)
	if !ok {
		add(validationMappingFormat, path)
		return
	}
	filePath := cfgval.String(m[rules.FieldPath])
	app := cfgval.String(m[rules.FieldApp])
	switch {
	case filePath == "" && app == "":
		add("%s requires a path or app", path)
	case filePath != "" && app != "":
		add("%s must use either path or app, not both", path)
	}
	if app == "" {
		return
	}
	if level := cfgval.String(m[rules.FieldLevel]); level != "" {
		if _, ok := checks.VersionLevel(level); !ok {
			add("%s.level %q is not one of %s", path, level, checks.VersionLevelSummary)
		}
	}
	if _, ok := appVersionChecks[app]; !ok {
		add("%s app %q has no app version command", path, app)
	}
}

func treeAppVersionChecks(checkNames map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for name := range checkNames {
		app, ok := strings.CutSuffix(name, ServiceMonitorVersionCheckSuffix)
		if ok && app != "" {
			out[app] = struct{}{}
		}
	}
	return out
}

func validateProbe(v any, path string, checkNames, systemMetricChecks map[string]struct{}, allowSystemMetric bool, add addFunc) {
	m, ok := v.(map[string]any)
	if !ok {
		add(validationMappingFormat, path)
		return
	}
	if ref := cfgval.String(m[rules.FieldCheck]); ref != "" {
		if _, ok := checkNames[ref]; !ok {
			add("%s references unknown check %q", path, ref)
		} else if _, isSys := systemMetricChecks[ref]; isSys && !allowSystemMetric {
			add("%s references system metric check %q, which is only allowed in alert rules", path, ref)
		}
		return
	}
	if len(m) != 1 {
		add("%s inline probe must have exactly one type key", path)
		return
	}
	for typ, raw := range m {
		fields, ok := raw.(map[string]any)
		if !ok {
			add("%s.%s must be a mapping", path, typ)
			continue
		}
		entry := maps.Clone(fields)
		entry[checks.CheckKeyType] = typ
		if typ == checks.CheckTypeMetric {
			validateMetric(entry, path+"."+typ, allowSystemMetric, add)
			continue
		}
		if !validateSingleShotCheckFields(path+"."+typ, typ, entry, "", add) {
			add("%s inline probe type %q is unknown", path, typ)
		}
	}
}

func validateState(v any, field string, valid map[string]struct{}, list, path string, add addFunc) {
	m, ok := v.(map[string]any)
	if !ok {
		add(validationMappingFormat, path)
		return
	}
	st := cfgval.String(m[field])
	if st == "" {
		return // defaulted
	}
	if _, ok := valid[st]; !ok {
		add("%s.%s %q is not one of %s", path, field, st, list)
	}
}

func validateMetric(entry map[string]any, path string, allowSystem bool, add addFunc) {
	scope := cfgval.String(entry[rules.FieldScope])
	if scope == "" {
		scope = checks.MetricScopeService
	}
	catalog, ok := metricCatalog[scope]
	if !ok {
		add("%s scope %q is not service or system", path, scope)
		return
	}
	name := cfgval.String(entry[rules.FieldName])
	known := false
	if name == "" {
		add("%s requires a metric name", path)
	} else if _, ok := catalog[name]; !ok {
		add("%s metric %q is not in the %s catalog", path, name, scope)
	} else {
		known = true
	}
	if op := cfgval.String(entry[rules.FieldOp]); op != "" {
		if !cfgval.IsCompareOp(op) {
			add("%s op %q is not one of %s", path, op, cfgval.CompareOpSummary)
		}
	}
	value := cfgval.String(entry[rules.FieldValue])
	if !parseMetricValue(value) {
		add("%s value %q must be a number with an optional trailing %%", path, value)
	} else if known {
		// Form must match: a "%" threshold needs a percentage form, a bare number
		// an absolute form.
		form := metricForms[name]
		if strings.HasSuffix(strings.TrimSpace(value), cfgval.PercentSuffix) {
			if !form.percent {
				add("%s uses a %% threshold but metric %q has no percentage form", path, name)
			}
		} else if !form.absolute {
			add("%s uses an absolute threshold but metric %q has no absolute form", path, name)
		}
	}
	if scope == checks.MetricScopeSystem && !allowSystem {
		add("%s scope: system metric is only allowed in alert rules", path)
	}
}

type valAction struct {
	typ     string
	message string
}

// ruleActions returns a rule's actions, supporting both the single
// `then: {action, message}` and the multi `then: {actions: [...]}` forms.
func ruleActions(then map[string]any) []valAction {
	if list, ok := then[rules.RuleFieldActions].([]any); ok {
		out := make([]valAction, 0, len(list))
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				out = append(out, valAction{typ: cfgval.String(m[rules.RuleFieldType]), message: cfgval.String(m[rules.RuleFieldMessage])})
			}
		}
		return out
	}
	return []valAction{{typ: cfgval.String(then[rules.RuleFieldAction]), message: cfgval.String(then[rules.RuleFieldMessage])}}
}

func collectCheckNames(tree map[string]any) map[string]struct{} {
	names := map[string]struct{}{}
	for _, section := range []string{sectionChecks, sectionPreflight} {
		if entries, ok := tree[section].(map[string]any); ok {
			for name := range entries {
				names[name] = struct{}{}
			}
		}
	}
	return names
}

// collectSystemMetricChecks returns the names of checks that are scope:system
// metrics, so a remediation rule referencing one (via failed/active) can be
// flagged.
func collectSystemMetricChecks(tree map[string]any) map[string]struct{} {
	names := map[string]struct{}{}
	for _, section := range []string{sectionChecks, sectionPreflight} {
		entries, ok := tree[section].(map[string]any)
		if !ok {
			continue
		}
		for name, raw := range entries {
			if e, ok := raw.(map[string]any); ok && cfgval.String(e[checks.CheckKeyType]) == checks.CheckTypeMetric && cfgval.String(e[checks.CheckKeyScope]) == checks.MetricScopeSystem {
				names[name] = struct{}{}
			}
		}
	}
	return names
}

func presentOperators(node map[string]any) []string {
	var present []string
	for _, op := range conditionOperators {
		if _, ok := node[op]; ok {
			present = append(present, op)
		}
	}
	return present
}

func parseMetricValue(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.HasSuffix(s, cfgval.PercentSuffix) {
		_, ok := cfgval.Percent(s)
		return ok
	}
	_, ok := cfgval.Float(s)
	return ok
}
