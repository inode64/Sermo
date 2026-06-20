package cli

import (
	"fmt"
	"sort"

	"sermo/internal/config"
)

// requireService reports an unknown-service error unless name is configured.
// It returns exitSuccess when the service exists.
func (a App) requireService(opts options, cfg *config.Config, name string) int {
	if _, ok := cfg.Services[name]; !ok {
		return a.fail(opts, fmt.Sprintf("unknown service %q", name))
	}
	return exitSuccess
}

// resolveService resolves name into its flat tree, printing the scoped
// resolution issues on failure. It returns exitSuccess when resolution is clean.
func (a App) resolveService(opts options, cfg *config.Config, name string) (config.Resolved, int) {
	resolved, errs := cfg.Resolve(name)
	if len(errs) > 0 {
		a.printIssues(opts, scopedIssues(name, errs))
		return config.Resolved{}, exitConfigInvalid
	}
	return resolved, exitSuccess
}

func (a App) loadConfig(opts options) (*config.Config, int) {
	globalPath := opts.globalPath()
	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("load config failed: %v", err))
		return nil, exitRuntimeError
	}
	return cfg, exitSuccess
}

func sortedUnique[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
