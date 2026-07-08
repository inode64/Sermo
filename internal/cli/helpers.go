package cli

import (
	"fmt"
	"sort"

	"sermo/internal/config"
)

// canonicalService resolves name to the configured service name, accepting
// service aliases and safe catalog aliases.
func (a App) canonicalService(opts options, cfg *config.Config, name string) (string, int) {
	canonical, ok := cfg.CanonicalServiceName(name)
	if !ok {
		return "", a.fail(opts, fmt.Sprintf(cliUnknownServiceFormat, name))
	}
	return canonical, exitSuccess
}

func canonicalServiceIfKnown(cfg *config.Config, name string) string {
	if canonical, ok := cfg.CanonicalServiceName(name); ok {
		return canonical
	}
	return name
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
