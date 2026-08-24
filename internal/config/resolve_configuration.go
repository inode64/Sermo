package config

import "sermo/internal/checks"

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
// command into a periodic advisory check. The original preflight entry remains
// required and continues to block unsafe start/restart/reload/resume actions;
// only the monitoring copy is warning-grade, because a running service with an
// invalid next configuration is degraded but still available.
func expandConfigurationCheck(tree map[string]any) []string {
	preflight, _ := tree[sectionPreflight].(map[string]any)
	entry, _ := preflight[ServiceMonitorKeyConfig].(map[string]any)
	if entry == nil {
		return nil
	}

	generated := cloneMap(entry)
	generated[checks.CheckKeySeverity] = checks.SeverityWarning
	if _, present := generated[EntryKeyInterval]; !present {
		generated[EntryKeyInterval] = DefaultConfigurationCheckInterval
	}
	if err := injectGenerated(tree, sectionChecks, ConfigurationCheckName, "check", "configuration", generated); err != "" {
		return []string{err}
	}
	return nil
}
