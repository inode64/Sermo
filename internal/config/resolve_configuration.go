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
// monitoring copy is warning-grade unless the entry declares its own severity:
// a running service with an invalid next configuration is degraded but still
// available, and a service whose next reload or restart would take it down
// (a web server) declares `severity: error`. Either way it never counts
// against availability: it judges the next start, not the running service.
func expandConfigurationCheck(tree map[string]any) []string {
	preflight, _ := tree[sectionPreflight].(map[string]any)
	entry, _ := preflight[ServiceMonitorKeyConfig].(map[string]any)
	if entry == nil {
		return nil
	}

	generated := cloneMap(entry)
	if _, declared := generated[checks.CheckKeySeverity]; !declared {
		generated[checks.CheckKeySeverity] = string(severity.Warning)
	}
	if _, present := generated[EntryKeyInterval]; !present {
		generated[EntryKeyInterval] = DefaultConfigurationCheckInterval
	}
	if err := injectGenerated(tree, sectionChecks, ConfigurationCheckName, "check", "configuration", generated); err != "" {
		return []string{err}
	}
	return nil
}
