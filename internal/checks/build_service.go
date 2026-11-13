package checks

import (
	"errors"
	"fmt"
	"regexp"

	"sermo/internal/cfgval"
	"sermo/internal/metrics"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// ServiceCheckFields keeps YAML validation and check construction in agreement.
func ServiceCheckFields(entry map[string]any) error {
	_, explicit := entry[CheckKeyService]
	if result, present := entry[CheckKeyExpectResult]; present {
		if !explicit {
			return errors.New("expect_result requires an explicit service")
		}
		if _, state := entry[CheckKeyExpect]; state {
			return errors.New("expect and expect_result are mutually exclusive")
		}
		if cfgval.AsString(entry[CheckKeyBackend]) != string(servicemgr.BackendSystemd) || cfgval.AsString(result) == "" {
			return errors.New("expect_result requires systemd and a non-empty string")
		}
	} else {
		state := cfgval.AsString(entry[CheckKeyExpect])
		switch servicemgr.Status(state) {
		case servicemgr.StatusActive, servicemgr.StatusInactive, servicemgr.StatusPaused, servicemgr.StatusFailed, servicemgr.StatusUnknown:
		case "":
			return errors.New("expect is required for a service check")
		default:
			return fmt.Errorf("expect %q is not one of %s", state, servicemgr.StatusSummary)
		}
	}
	if explicit {
		return unitServiceFields(entry)
	}
	return nil
}

// buildServiceCheck builds a check on a service-manager unit's expected state.
func buildServiceCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	if err := ServiceCheckFields(entry); err != nil {
		return nil, err.Error()
	}
	if _, present := entry[CheckKeyService]; present {
		return buildUnitServiceCheck(b, entry, deps)
	}
	expect := cfgval.AsString(entry[CheckKeyExpect])
	if deps.Status == nil {
		return nil, "service check needs backend detection, unavailable here"
	}
	return serviceCheck{base: b, expect: expect, status: deps.Status}, ""
}

// Metric-check scopes (the `scope:` selector of a metric check). Exported so
// config validation checks the same scope vocabulary the builder accepts.
const (
	MetricScopeService = string(metrics.ScopeService)
	MetricScopeSystem  = string(metrics.ScopeSystem)
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
	return metricCheck{base: b, scope: scope, metric: name, op: op, value: cfgval.String(entry[CheckKeyValue]), source: deps.Metrics, grades: metricGrades(b.levels)}, ""
}

// buildProcessCheck builds a check on processes matching an exe/user/cmd selector.
func buildProcessCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	exe := cfgval.AsString(entry[CheckKeyExe])
	exes := cfgval.StringList(entry[CheckKeyExeAny])
	if exe != "" {
		exes = []string{exe}
	}
	user := cfgval.AsString(entry[CheckKeyUser])
	cmd, _, err := ProcessCommandPattern(entry)
	if err != nil {
		return nil, "process check " + err.Error()
	}
	if len(exes) == 0 && cmd == nil {
		return nil, "process check requires exe, exe_any or cmd"
	}
	if deps.ProcessesAny == nil {
		return nil, "process check needs process discovery, unavailable here"
	}
	expect := cfgval.AsString(entry[CheckKeyState])
	if expect == "" {
		expect = process.StateRunning
	}
	return processCheck{
		base: b, exes: exes, user: user, cmd: cmd, expect: expect,
		observeAny: deps.ProcessesAny, stale: deps.StaleBinaries, replacedAny: deps.ProcessesReplaced,
	}, ""
}

// ProcessCommandPattern prepares the optional cmd predicate for state checks
// and per-PID watches; validation uses the same parser as both builders.
// The bool reports whether cmd was configured, including malformed values.
func ProcessCommandPattern(entry map[string]any) (*regexp.Regexp, bool, error) {
	value, present := entry[CheckKeyCmd]
	if !present {
		return nil, false, nil
	}
	pattern, ok := value.(string)
	if !ok || pattern == "" {
		return nil, true, errors.New("cmd must be a non-empty regex")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, true, fmt.Errorf("cmd is not a valid regex: %w", err)
	}
	return re, true, nil
}

// buildStaleBinaryCheck builds a check reporting this service's processes whose
// binary was replaced on disk. It takes no entry fields: the selectors it
// inspects are the service's own `processes:`/`pidfile:` declarations, so there
// is nothing left to configure per check.
func buildStaleBinaryCheck(b base, deps Deps) (Check, string) {
	if deps.StaleBinaries == nil {
		return nil, "stale_binary check needs process discovery, unavailable here"
	}
	return staleBinaryCheck{base: b, stale: deps.StaleBinaries}, ""
}

// buildStraysCheck builds a check reporting this service's control-group members
// that no selector claims. What counts as a stray follows from the service's own
// `processes:`/`pidfile:` declarations and the init unit's control group, so the
// only things configurable are the two bounds above which it fails.
//
// Both default to "any stray fails", which is what the injected instance uses.
// `max_increase` needs `within` because growth is measured over wall-clock time,
// not over cycles — see straysCheck.
func buildStraysCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	if deps.Strays == nil {
		return nil, "strays check needs process discovery, unavailable here"
	}
	check := straysCheck{base: b, strays: deps.Strays}
	if raw, present := entry[CheckKeyMax]; present {
		n, ok := cfgval.Int(raw)
		if !ok || n < 0 {
			return nil, "strays check max must be a non-negative integer"
		}
		check.max, check.hasMax = float64(n), true
	}
	raw, hasIncrease := entry[CheckKeyMaxIncrease]
	if !hasIncrease {
		if _, hasWindow := entry[CheckKeyWithin]; hasWindow {
			return nil, "strays check within requires max_increase"
		}
		return check, ""
	}
	n, ok := cfgval.Int(raw)
	if !ok || n < 1 {
		return nil, "strays check max_increase must be a positive integer"
	}
	window := cfgval.Duration(entry[CheckKeyWithin])
	if window <= 0 {
		return nil, "strays check max_increase requires within as a positive duration"
	}
	check.maxIncrease, check.window = float64(n), window
	check.state = &counterWindow{}
	return check, ""
}
