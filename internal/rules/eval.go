package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/metrics"
)

// ChangeContext describes the changed: leaf that made the current rule true.
// It is runtime-only metadata for alert message expansion; the condition's
// boolean result remains the evaluator's source of truth.
type ChangeContext struct {
	Path       string
	App        string
	Library    string
	Level      string
	LevelValue int
	OldVersion string
	NewVersion string
}

// Evaluator evaluates condition trees against the per-cycle check cache and, for
// inline conditions, runs probes at most once each (memoized for the cycle).
type Evaluator struct {
	// Cache holds the results of the service's named checks for this cycle,
	// keyed by check name (for failed/active references). ResolveRef may add
	// lazy preflight results to this map.
	Cache map[string]checks.Result
	// ResolveRef resolves additional named checks (for example preflight entries)
	// when a failed/active reference is not present in Cache. The bool reports
	// whether the name exists; false preserves the usual unknown-check error.
	ResolveRef RefResolver
	// Deps builds inline probes (tcp/command/service/file).
	Deps checks.Deps
	// Changed reports whether the file at the given path differs from a baseline
	// tracked across cycles. Injected by the
	// worker; nil makes every `changed:` condition false.
	Changed func(path string) (bool, error)
	// ChangedVersion reports whether the named app's version (its version command
	// output reduced to version_short and truncated to level: 1=major, 2=minor,
	// 3=patch) differs from a baseline tracked across cycles. Injected by the
	// worker; nil makes every `changed: {app}` condition false.
	ChangedVersion func(ctx context.Context, app string, level int) (bool, error)
	// Change is populated when a changed: leaf evaluates true, so callers can
	// expand rule messages with the concrete changed path/app/library.
	Change ChangeContext

	memo map[string]checks.Result
}

// RefResolver lazily resolves a named check reference outside the per-cycle
// monitoring cache, such as a preflight entry referenced by a guard rule.
type RefResolver func(context.Context, string) (checks.Result, bool, error)

// NewCheckResolver returns a resolver over a static set of built checks. Results
// are memoized inside the returned resolver, so a referenced preflight check runs
// at most once for the caller's evaluation pass.
func NewCheckResolver(built []checks.Built, maxParallel int) RefResolver {
	if len(built) == 0 {
		return nil
	}
	byName := make(map[string]checks.Built, len(built))
	for _, b := range built {
		byName[b.Check.Name()] = b
	}
	memo := make(map[string]checks.Result, len(built))
	return func(ctx context.Context, name string) (checks.Result, bool, error) {
		if res, ok := memo[name]; ok {
			return res, true, nil
		}
		b, ok := byName[name]
		if !ok {
			return checks.Result{}, false, nil
		}
		results := checks.Run(ctx, []checks.Built{b}, maxParallel)
		if len(results) == 0 {
			return checks.Result{}, true, fmt.Errorf("check %q produced no result", name)
		}
		memo[name] = results[0]
		return results[0], true, nil
	}
}

// Eval evaluates a condition node, returning its boolean value. An unresolvable
// reference or an unsupported condition is an error, not a silent false, so the
// caller (guard/remediation) can treat it conservatively.
func (e *Evaluator) Eval(ctx context.Context, node map[string]any) (bool, error) {
	if len(node) == 0 {
		return false, fmt.Errorf("empty condition")
	}

	if v, ok := node[ConditionAnd]; ok {
		return e.evalList(ctx, v, true)
	}
	if v, ok := node[ConditionOr]; ok {
		return e.evalList(ctx, v, false)
	}
	if v, ok := node[ConditionNot]; ok {
		child, ok := v.(map[string]any)
		if !ok {
			return false, fmt.Errorf("not: must be a condition mapping")
		}
		r, err := e.Eval(ctx, child)
		return !r, err
	}
	if v, ok := node[ConditionFailed]; ok {
		res, err := e.probe(ctx, v)
		return !res.OK, err
	}
	if v, ok := node[ConditionActive]; ok {
		res, err := e.probe(ctx, v)
		return res.OK, err
	}
	if v, ok := node[ConditionFile]; ok {
		return e.evalFile(ctx, v)
	}
	if v, ok := node[ConditionCommand]; ok {
		return e.evalInline(ctx, checks.CheckTypeCommand, v)
	}
	if v, ok := node[ConditionService]; ok {
		return e.evalService(ctx, v)
	}
	if v, ok := node[ConditionProcess]; ok {
		return e.evalProcess(v)
	}
	if v, ok := node[ConditionMetric]; ok {
		return e.evalMetric(v)
	}
	if v, ok := node[ConditionChanged]; ok {
		return e.evalChanged(ctx, v)
	}
	return false, fmt.Errorf("condition has no recognized operator")
}

// condMap asserts a condition operand is a mapping, returning a "<label> must be
// a mapping" error otherwise. It collapses the assertion repeated by every leaf
// evaluator into one call.
func condMap(v any, label string) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a mapping", label)
	}
	return m, nil
}

func (e *Evaluator) evalList(ctx context.Context, v any, and bool) (bool, error) {
	items, ok := v.([]any)
	if !ok || len(items) == 0 {
		return false, fmt.Errorf("and/or requires a non-empty list")
	}
	for _, item := range items {
		node, ok := item.(map[string]any)
		if !ok {
			return false, fmt.Errorf("and/or item must be a condition mapping")
		}
		r, err := e.Eval(ctx, node)
		if err != nil {
			return false, err
		}
		if and && !r {
			return false, nil // short-circuit AND
		}
		if !and && r {
			return true, nil // short-circuit OR
		}
	}
	return and, nil // AND: all true; OR: none true
}

// probe resolves a failed/active operand: either {check: name} from the cache,
// or an inline {<type>: params} probe built and run once (memoized).
func (e *Evaluator) probe(ctx context.Context, v any) (checks.Result, error) {
	m, err := condMap(v, "probe")
	if err != nil {
		return checks.Result{}, err
	}
	if ref := cfgval.AsString(m[FieldCheck]); ref != "" {
		res, ok := e.Cache[ref]
		if !ok {
			if e.ResolveRef != nil {
				var err error
				res, ok, err = e.ResolveRef(ctx, ref)
				if err != nil {
					return checks.Result{}, err
				}
				if ok {
					if e.Cache == nil {
						e.Cache = map[string]checks.Result{}
					}
					e.Cache[ref] = res
				}
			}
		}
		if !ok {
			return checks.Result{}, fmt.Errorf("unknown check %q", ref)
		}
		return res, nil
	}

	entry, name, err := inlineEntry(m)
	if err != nil {
		return checks.Result{}, err
	}
	return e.runInline(ctx, name, entry, m)
}

func (e *Evaluator) evalFile(ctx context.Context, v any) (bool, error) {
	m, err := condMap(v, "file condition")
	if err != nil {
		return false, err
	}
	path := cfgval.AsString(m[FieldPath])
	if path == "" {
		return false, fmt.Errorf("file condition requires a path")
	}
	wantExists := true
	if b, ok := m[FieldExists].(bool); ok {
		wantExists = b
	}
	res, err := e.runInline(ctx, checks.CheckTypeFile, map[string]any{FieldType: checks.CheckTypeFileExists, FieldPath: path}, m)
	if err != nil {
		return false, err
	}
	return res.OK == wantExists, nil
}

func (e *Evaluator) evalService(ctx context.Context, v any) (bool, error) {
	m, err := condMap(v, "service condition")
	if err != nil {
		return false, err
	}
	state := cfgval.AsString(m[FieldState])
	if state == "" {
		return false, fmt.Errorf("service condition requires a state")
	}
	res, err := e.runInline(ctx, checks.CheckTypeService, map[string]any{FieldType: checks.CheckTypeService, FieldExpect: state}, m)
	if err != nil {
		return false, err
	}
	return res.OK, nil
}

// evalProcess is true when the observed state of processes matching the
// exe/user selector equals the requested state (default running).
// With no process source it is false.
func (e *Evaluator) evalProcess(v any) (bool, error) {
	m, err := condMap(v, "process condition")
	if err != nil {
		return false, err
	}
	if e.Deps.Processes == nil {
		return false, nil
	}
	want := cfgval.AsString(m[FieldState])
	if want == "" {
		want = ProcessStateRunning
	}
	return e.Deps.Processes(cfgval.AsString(m[FieldExe]), cfgval.AsString(m[FieldUser])) == want, nil
}

// evalMetric reads a sampled metric and compares it to the threshold.
// With no metric source, or a not-ready rate metric, it is false
// so a remediation never fires on an unavailable value.
func (e *Evaluator) evalMetric(v any) (bool, error) {
	m, err := condMap(v, "metric condition")
	if err != nil {
		return false, err
	}
	if e.Deps.Metrics == nil {
		return false, nil
	}
	scope := cfgval.AsString(m[FieldScope])
	if scope == "" {
		scope = checks.MetricScopeService
	}
	reading, ok := e.Deps.Metrics(scope, cfgval.AsString(m[FieldName]))
	if !ok {
		return false, nil
	}
	return metrics.Compare(reading, cfgval.AsString(m[FieldOp]), cfgval.String(m[FieldValue]))
}

// evalChanged is true when the watched signal differs from the baseline tracked
// across cycles. With `app` it compares the app's version (truncated to an
// optional major/minor/patch level, default patch); otherwise it compares the
// file fingerprint at `path`. With no corresponding source it is false, so a
// remediation never fires on an unavailable signal.
func (e *Evaluator) evalChanged(ctx context.Context, v any) (bool, error) {
	m, err := condMap(v, "changed condition")
	if err != nil {
		return false, err
	}
	if app := cfgval.AsString(m[FieldApp]); app != "" {
		level := 3 // patch: any a.b.c change fires
		levelName := checks.VersionLevelPatch
		if name := cfgval.AsString(m[FieldLevel]); name != "" {
			lvl, ok := checks.VersionLevel(name)
			if !ok {
				return false, fmt.Errorf("changed condition level %q is not one of %s", name, checks.VersionLevelSummary)
			}
			level = lvl
			levelName = name
		}
		if e.ChangedVersion == nil {
			return false, nil
		}
		changed, err := e.ChangedVersion(ctx, app, level)
		if changed {
			e.Change = ChangeContext{App: app, Level: levelName, LevelValue: level}
		}
		return changed, err
	}
	path := cfgval.AsString(m[FieldPath])
	if path == "" {
		return false, fmt.Errorf("changed condition requires a path or app")
	}
	if e.Changed == nil {
		return false, nil
	}
	changed, err := e.Changed(path)
	if changed {
		e.Change = ChangeContext{Path: path, Library: cfgval.AsString(m[FieldLibrary])}
	}
	return changed, err
}

// evalInline builds and runs a leaf check whose truth is the check's OK.
func (e *Evaluator) evalInline(ctx context.Context, typ string, v any) (bool, error) {
	m, err := condMap(v, typ+" condition")
	if err != nil {
		return false, err
	}
	entry := map[string]any{FieldType: typ}
	for k, val := range m {
		entry[k] = val
	}
	res, err := e.runInline(ctx, typ, entry, m)
	if err != nil {
		return false, err
	}
	return res.OK, nil
}

// runInline builds, runs and memoizes an inline check keyed by its normalized
// parameters so identical probes run at most once per cycle.
func (e *Evaluator) runInline(ctx context.Context, name string, entry map[string]any, keyParams map[string]any) (checks.Result, error) {
	key := name + ":" + normalizeKey(keyParams)
	if res, ok := e.memo[key]; ok {
		return res, nil
	}
	check, err := checks.BuildInline(name, entry, e.Deps)
	if err != nil {
		return checks.Result{}, err
	}
	res := check.Run(ctx)
	if e.memo == nil {
		e.memo = map[string]checks.Result{}
	}
	e.memo[key] = res
	return res, nil
}

// inlineEntry converts an inline {<type>: params} operand into a check entry.
func inlineEntry(m map[string]any) (map[string]any, string, error) {
	if len(m) != 1 {
		return nil, "", fmt.Errorf("inline probe must have exactly one type key")
	}
	for k, v := range m {
		params, ok := v.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("inline %s probe must be a mapping", k)
		}
		entry := map[string]any{FieldType: k}
		for pk, pv := range params {
			entry[pk] = pv
		}
		return entry, k, nil
	}
	return nil, "", fmt.Errorf("empty inline probe")
}

func normalizeKey(m map[string]any) string {
	data, err := json.Marshal(m) // Go sorts map keys in JSON output
	if err != nil {
		return fmt.Sprintf("%v", m)
	}
	return string(data)
}

// Guard reports whether any guard rule blocks action, returning the blocking
// rule's message. Guards are evaluated in name order; the first
// blocking guard wins. An evaluation error is returned so the caller can fail
// safe rather than silently proceed.
func Guard(ctx context.Context, ruleSet []Rule, action string, ev *Evaluator) (blocked bool, reason string, err error) {
	for _, r := range ruleSet {
		if r.Type != RuleGuard || !slices.Contains(r.Blocks, action) {
			continue
		}
		ok, err := ev.Eval(ctx, r.If)
		if err != nil {
			return false, "", fmt.Errorf("guard %s: %w", r.Name, err)
		}
		if ok {
			reason := r.Primary().Message
			if reason == "" {
				reason = "blocked by guard " + r.Name
			}
			return true, reason, nil
		}
	}
	return false, "", nil
}
