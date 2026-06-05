package rules

import (
	"context"
	"encoding/json"
	"fmt"

	"sermo/internal/checks"
)

// Evaluator evaluates condition trees against the per-cycle check cache and, for
// inline conditions, runs probes at most once each (memoized for the cycle, per
// section 14).
type Evaluator struct {
	// Cache holds the results of the service's named checks for this cycle,
	// keyed by check name (for failed/active references).
	Cache map[string]checks.Result
	// Deps builds inline probes (tcp/command/service/file).
	Deps checks.Deps

	memo map[string]checks.Result
}

// Eval evaluates a condition node, returning its boolean value. An unresolvable
// reference or an unsupported condition is an error, not a silent false, so the
// caller (guard/remediation) can treat it conservatively.
func (e *Evaluator) Eval(ctx context.Context, node map[string]any) (bool, error) {
	if len(node) == 0 {
		return false, fmt.Errorf("empty condition")
	}

	if v, ok := node["and"]; ok {
		return e.evalList(ctx, v, true)
	}
	if v, ok := node["or"]; ok {
		return e.evalList(ctx, v, false)
	}
	if v, ok := node["not"]; ok {
		child, ok := v.(map[string]any)
		if !ok {
			return false, fmt.Errorf("not: must be a condition mapping")
		}
		r, err := e.Eval(ctx, child)
		return !r, err
	}
	if v, ok := node["failed"]; ok {
		res, err := e.probe(ctx, v)
		return !res.OK, err
	}
	if v, ok := node["active"]; ok {
		res, err := e.probe(ctx, v)
		return res.OK, err
	}
	if v, ok := node["file"]; ok {
		return e.evalFile(ctx, v)
	}
	if v, ok := node["command"]; ok {
		return e.evalInline(ctx, "command", v)
	}
	if v, ok := node["service"]; ok {
		return e.evalService(ctx, v)
	}
	if _, ok := node["process"]; ok {
		return false, fmt.Errorf("process condition is not implemented in this slice")
	}
	if _, ok := node["metric"]; ok {
		return false, fmt.Errorf("metric condition is not implemented in this slice")
	}
	return false, fmt.Errorf("condition has no recognized operator")
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
	m, ok := v.(map[string]any)
	if !ok {
		return checks.Result{}, fmt.Errorf("probe must be a mapping")
	}
	if ref := asString(m["check"]); ref != "" {
		res, ok := e.Cache[ref]
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
	m, ok := v.(map[string]any)
	if !ok {
		return false, fmt.Errorf("file condition must be a mapping")
	}
	path := asString(m["path"])
	if path == "" {
		return false, fmt.Errorf("file condition requires a path")
	}
	wantExists := true
	if b, ok := m["exists"].(bool); ok {
		wantExists = b
	}
	res, err := e.runInline(ctx, "file", map[string]any{"type": "file_exists", "path": path}, m)
	if err != nil {
		return false, err
	}
	return res.OK == wantExists, nil
}

func (e *Evaluator) evalService(ctx context.Context, v any) (bool, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return false, fmt.Errorf("service condition must be a mapping")
	}
	state := asString(m["state"])
	if state == "" {
		return false, fmt.Errorf("service condition requires a state")
	}
	res, err := e.runInline(ctx, "service", map[string]any{"type": "service", "expect": state}, m)
	if err != nil {
		return false, err
	}
	return res.OK, nil
}

// evalInline builds and runs a leaf check whose truth is the check's OK.
func (e *Evaluator) evalInline(ctx context.Context, typ string, v any) (bool, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return false, fmt.Errorf("%s condition must be a mapping", typ)
	}
	entry := map[string]any{"type": typ}
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
// parameters so identical probes run at most once per cycle (section 14).
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
		entry := map[string]any{"type": k}
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
// rule's message (section 17). Guards are evaluated in name order; the first
// blocking guard wins. An evaluation error is returned so the caller can fail
// safe rather than silently proceed.
func Guard(ctx context.Context, ruleSet []Rule, action string, ev *Evaluator) (blocked bool, reason string, err error) {
	for _, r := range ruleSet {
		if r.Type != RuleGuard || !contains(r.Blocks, action) {
			continue
		}
		ok, err := ev.Eval(ctx, r.If)
		if err != nil {
			return false, "", fmt.Errorf("guard %s: %w", r.Name, err)
		}
		if ok {
			reason := r.Then.Message
			if reason == "" {
				reason = "blocked by guard " + r.Name
			}
			return true, reason, nil
		}
	}
	return false, "", nil
}
