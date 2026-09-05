package cli

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"

	"sermo/internal/config"
)

// readYAMLMap peeks at one wizard-managed document without applying the full
// config loader contract. Cleanup discovery is best-effort, so unreadable or
// malformed documents are ignored.
func readYAMLMap(path string) map[string]any {
	data, err := os.ReadFile(path) //nolint:gosec // G304: wizard-owned config path under a configured directory
	if err != nil {
		return nil
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	return doc
}

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

// renderServiceList prints the shared tail of the per-service list commands
// (locks, processes): warnings to stderr, JSON on --json, an empty notice
// unless --quiet, else one formatted line per item.
func renderServiceList[T any](a App, opts options, service, jsonKey string, items []T, warnings []string, emptyFormat string, format func(T) string) int {
	for _, w := range warnings {
		fmt.Fprintf(a.Stderr, cliWarningFormat, w)
	}
	if opts.json {
		writeJSON(a.Stdout, map[string]any{cliJSONKeyService: service, jsonKey: items})
		return exitSuccess
	}
	if len(items) == 0 {
		if !opts.quiet {
			fmt.Fprintf(a.Stdout, emptyFormat, service)
		}
		return exitSuccess
	}
	for _, item := range items {
		fmt.Fprintln(a.Stdout, format(item))
	}
	return exitSuccess
}
