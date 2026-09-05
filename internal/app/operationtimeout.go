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
// stop_policy, and raised again by any enabled host-watch probe budget. The
// HTTP write deadline is sized from this value so a manual probe can return
// after its check timeout instead of being cut off at the service-operation
// limit. Service-scoped watches are not included: they cannot be probed from
// the panel, and Resolve desugars them into checks before the tree is used.
func MaxOperationTimeout(cfg *config.Config, configured time.Duration) time.Duration {
	maxTO := operation.ResolveTimeout(configured, nil)
	if cfg == nil {
		return maxTO
	}
	for _, name := range cfg.SortedServiceNames() {
		doc := cfg.Services[name]
		if doc == nil || cfgval.Disabled(doc.Body) {
			continue
		}
		resolved, errs := cfg.Resolve(name)
		if len(errs) > 0 {
			continue
		}
		maxTO = max(maxTO, operation.ResolveTimeout(configured, resolved.Tree))
	}
	defaultTimeout := config.EngineDuration(cfg, config.EngineKeyDefaultTimeout, DefaultEngineCheckTimeout)
	if watches, _ := cfg.ResolveWatches(); len(watches) > 0 {
		maxTO = maxWatchProbeTimeout(maxTO, watches, defaultTimeout, configured)
	}
	return maxTO
}

func maxWatchProbeTimeout(maxTO time.Duration, raw any, defaultTimeout, operationTimeout time.Duration) time.Duration {
	watches, _ := raw.(map[string]any)
	for _, item := range watches {
		entry, _ := item.(map[string]any)
		if entry == nil || cfgval.Disabled(entry) {
			continue
		}
		check := checkMap(entry)
		if !ManualProbeCheckType(cfgval.String(check[checks.CheckKeyType])) {
			continue
		}
		maxTO = max(maxTO, checkProbeTimeout(check, defaultTimeout, operationTimeout))
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
	return operationTimeout
}
