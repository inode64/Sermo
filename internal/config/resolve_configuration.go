package config

import (
	"sermo/internal/checks"
	"sermo/internal/severity"
)

const (
	// ConfigurationCheckName is the reserved service check synthesized from
	// preflight.config. The app layer uses the stable name to expose the reason
	// behind an advisory service state without rerunning the command in a web
	// request.
	ConfigurationCheckName = "configuration"
	// DefaultConfigurationCheckInterval bounds application configuration tests
	// independently from the usually shorter service worker cycle.
	DefaultConfigurationCheckInterval = "15m"
)

// expandConfigurationCheck turns the application's existing preflight.config
// command into a periodic check. The original preflight entry remains required
// and continues to block unsafe start/restart/reload/resume actions. The
// monitoring copy is error-grade unless the entry declares its own severity:
// while it fails, the required preflight refuses every operation on the
// service, automatic recovery included, so the operator has to hear about it
// rather than find an amber row. It never counts against availability, and it
// leaves a running service warning rather than failed: it judges the next
// start, not the running service.
func expandConfigurationCheck(tree map[string]any) []string {
	preflight, _ := tree[sectionPreflight].(map[string]any)
	entry, _ := preflight[ServiceMonitorKeyConfig].(map[string]any)
	if entry == nil {
		return nil
	}

	generated := cloneMap(entry)
	if _, declared := generated[checks.CheckKeySeverity]; !declared {
		generated[checks.CheckKeySeverity] = string(severity.Error)
	}
	if _, present := generated[EntryKeyInterval]; !present {
		generated[EntryKeyInterval] = DefaultConfigurationCheckInterval
	}
	if err := injectGenerated(tree, sectionChecks, ConfigurationCheckName, "check", "configuration", generated); err != "" {
		return []string{err}
	}
	return nil
}
