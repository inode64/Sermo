package checks

import (
	"sermo/internal/cfgval"
	"sermo/internal/process"
)

// buildServiceCheck builds a check on a service-manager unit's expected state.
func buildServiceCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	expect := cfgval.AsString(entry[CheckKeyExpect])
	if expect == "" {
		return nil, "service check requires expect"
	}
	if deps.Status == nil {
		return nil, "service check needs backend detection, unavailable here"
	}
	return serviceCheck{base: b, expect: expect, status: deps.Status}, ""
}

// Metric-check scopes (the `scope:` selector of a metric check). Exported so
// config validation checks the same scope vocabulary the builder accepts.
const (
	MetricScopeService = "service"
	MetricScopeSystem  = "system"
)

// buildMetricCheck builds a check comparing a sampled metric to a threshold.
func buildMetricCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	name := cfgval.AsString(entry[CheckKeyName])
	if name == "" {
		return nil, "metric check requires a name"
	}
	scope := cfgval.AsString(entry[CheckKeyScope])
	if scope == "" {
		scope = MetricScopeService
	}
	op := cfgval.AsString(entry[CheckKeyOp])
	if op == "" {
		return nil, "metric check requires an op"
	}
	if deps.Metrics == nil {
		return nil, "metric check needs a metric source, unavailable here"
	}
	return metricCheck{base: b, scope: scope, metric: name, op: op, value: cfgval.String(entry[CheckKeyValue]), source: deps.Metrics}, ""
}

// buildProcessCheck builds a check on processes matching an exe/user selector.
func buildProcessCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	exe := cfgval.AsString(entry[CheckKeyExe])
	exes := cfgval.StringList(entry[CheckKeyExeAny])
	if exe != "" {
		exes = []string{exe}
	}
	user := cfgval.AsString(entry[CheckKeyUser])
	if len(exes) == 0 {
		return nil, "process check requires exe or exe_any"
	}
	if deps.Processes == nil && deps.ProcessesAny == nil {
		return nil, "process check needs process discovery, unavailable here"
	}
	expect := cfgval.AsString(entry[CheckKeyState])
	if expect == "" {
		expect = process.StateRunning
	}
	return processCheck{base: b, exes: exes, user: user, expect: expect, observe: deps.Processes, observeAny: deps.ProcessesAny}, ""
}
