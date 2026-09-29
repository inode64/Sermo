package app

import (
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/operation"
)

// MaxOperationTimeout returns the longest deadline any enabled web action may
// need: the configured engine operation timeout, raised per service by
// stop_policy, by the longest operator button timeout, by the synchronous
// also_apply cascade the web runs for a service, and raised again by any
// enabled host-watch probe budget and any configured mount unit's escalation
// budget. The
// HTTP write deadline is sized from this value so a manual probe can return
// after its check timeout instead of being cut off at the service-operation
// limit. Service-scoped watches are not included: they cannot be probed from
// the panel, and Resolve desugars them into checks before the tree is used.
func MaxOperationTimeout(cfg *config.Config, configured time.Duration) time.Duration {
	maxTO := operation.ResolveTimeout(configured, nil)
	if cfg == nil {
		return maxTO
	}
	names := cfg.EnabledServiceNames()
	timeouts := make(map[string]time.Duration, len(names))
	targets := make(map[string][]string, len(names))
	for i, resolution := range cfg.ResolveServices(names) {
		resolved, errs := resolution.Resolved, resolution.Errors
		if len(errs) > 0 {
			continue
		}
		timeout := operation.ResolveTimeout(configured, resolved.Tree)
		timeouts[names[i]] = timeout
		targets[names[i]] = config.CascadeTargets(resolved.Tree)
		maxTO = max(maxTO, timeout)
		for _, button := range serviceButtons(resolved.Tree) {
			maxTO = max(maxTO, button.timeout)
		}
	}
	maxTO = max(maxTO, maxCascadeTimeout(timeouts, targets, operation.ResolveTimeout(configured, nil)))
	maxTO = max(maxTO, maxMountActionTimeout(cfg, configured))
	defaultTimeout := config.EngineDuration(cfg, config.EngineKeyDefaultTimeout, DefaultEngineCheckTimeout)
	watches, _ := cfg.ResolveWatches()
	return maxWatchProbeTimeout(maxTO, watches, defaultTimeout, configured)
}

// maxCascadeTimeout is the longest also_apply cascade any enabled service can
// start from the web. A member without a resolved timeout (disabled or invalid)
// counts with the default one.
func maxCascadeTimeout(timeouts map[string]time.Duration, targets map[string][]string, fallback time.Duration) time.Duration {
	lookup := func(service string) []string { return targets[service] }
	timeoutOf := func(service string) time.Duration {
		if timeout, ok := timeouts[service]; ok {
			return timeout
		}
		return fallback
	}
	var longest time.Duration
	for root, rootTargets := range targets {
		if len(rootTargets) == 0 {
			continue
		}
		longest = max(longest, cascadeBudget(root, lookup, timeoutOf))
	}
	return longest
}

func maxWatchProbeTimeout(maxTO time.Duration, watches map[string]any, defaultTimeout, operationTimeout time.Duration) time.Duration {
	for _, item := range watches {
		entry, _ := item.(map[string]any)
		if entry == nil || cfgval.Disabled(entry) {
			continue
		}
		check := checkMap(entry)
		if !ManualProbeCheckType(cfgval.String(check[checks.CheckKeyType])) {
			continue
		}
		timeout := checkProbeTimeout(check, defaultTimeout, operationTimeout)
		if cfgval.String(check[checks.CheckKeyType]) == checks.CheckTypeDiskIO {
			timeout = diskIOProbeBudget(timeout)
		}
		maxTO = max(maxTO, timeout)
	}
	return maxTO
}

// checkProbeTimeout is the budget of one manual watch probe: the check's own
// timeout, else the engine default, else the operation timeout. probeTimeout
// applies the same precedence so the HTTP write deadline and the probe context
// cannot disagree.
func checkProbeTimeout(check map[string]any, defaultTimeout, operationTimeout time.Duration) time.Duration {
	if timeout := cfgval.Duration(check[checks.CheckKeyTimeout]); timeout > 0 {
		return timeout
	}
	if defaultTimeout > 0 {
		return defaultTimeout
	}
	if operationTimeout > 0 {
		return operationTimeout
	}
	return DefaultEngineCheckTimeout
}
