package config

import (
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/process"
	"sermo/internal/rules"
)

func TestCatalogSNTPHasNoResidentProcess(t *testing.T) {
	resolved := resolveCatalogService(t, "sntp", backendSystemd)
	if got := resolvedProcessMode(resolved.Tree); got != ServiceProcessNone {
		t.Fatalf("process mode = %q, want none", got)
	}
	selectors, warnings := process.ParseSelectors(resolved.Tree)
	if len(selectors) != 0 || len(warnings) != 0 {
		t.Fatalf("selectors = %v, warnings = %v", selectors, warnings)
	}
	checks := nested(t, resolved.Tree, "checks")
	for _, name := range []string{fdsCheckName, staleBinaryCheckName} {
		if _, ok := checks[name]; ok {
			t.Errorf("nonresident service has generated check %s", name)
		}
	}
	service := nested(t, checks, "service")
	if cfgval.String(service["expect"]) != "active" || !cfgval.Bool(service["verify"]) {
		t.Fatalf("service verification = %v, want active", service)
	}
	parsed, warnings := rules.ParseRules(resolved.Tree)
	if len(parsed) != 0 || len(warnings) != 0 {
		t.Fatalf("SNTP must not acquire automatic actions: rules = %v, warnings = %v", parsed, warnings)
	}
}
