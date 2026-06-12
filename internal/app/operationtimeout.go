package app

import (
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/operation"
)

// MaxOperationTimeout returns the longest safe-operation deadline any enabled
// service may need: the configured engine timeout raised per service by
// stop_policy (operation.ResolveTimeout).
func MaxOperationTimeout(cfg *config.Config, configured time.Duration) time.Duration {
	maxTO := operation.ResolveTimeout(configured, nil)
	for _, name := range cfg.SortedServiceNames() {
		doc := cfg.Services[name]
		if doc == nil || cfgval.Disabled(doc.Body) {
			continue
		}
		resolved, errs := cfg.Resolve(name)
		if len(errs) > 0 {
			continue
		}
		if t := operation.ResolveTimeout(configured, resolved.Tree); t > maxTO {
			maxTO = t
		}
	}
	return maxTO
}
