package config

import (
	"testing"

	"sermo/internal/checks"
	"sermo/internal/rules"
)

func TestExpandConfigurationCheckCopiesConfigPreflightAsAdvisory(t *testing.T) {
	preflightEntry := map[string]any{
		checks.CheckKeyType:    checks.CheckTypeCommand,
		checks.CheckKeyCommand: []any{"nginx", "-t"},
		checks.CheckKeyTimeout: "30s",
	}
	tree := map[string]any{sectionPreflight: map[string]any{ServiceMonitorKeyConfig: preflightEntry}}

	if errs := expandConfigurationCheck(tree); len(errs) > 0 {
		t.Fatalf("expandConfigurationCheck: %v", errs)
	}

	entries, _ := tree[sectionChecks].(map[string]any)
	got, _ := entries[ConfigurationCheckName].(map[string]any)
	if got[checks.CheckKeyType] != checks.CheckTypeCommand || got[checks.CheckKeySeverity] != checks.SeverityWarning {
		t.Fatalf("generated configuration check = %#v", got)
	}
	if got[EntryKeyInterval] != DefaultConfigurationCheckInterval {
		t.Fatalf("generated interval = %v, want %q", got[EntryKeyInterval], DefaultConfigurationCheckInterval)
	}
	if preflightEntry[checks.CheckKeySeverity] != nil || preflightEntry[EntryKeyInterval] != nil {
		t.Fatalf("preflight entry was mutated: %#v", preflightEntry)
	}
	command := got[checks.CheckKeyCommand].([]any)
	command[0] = "changed"
	if preflightEntry[checks.CheckKeyCommand].([]any)[0] != "nginx" {
		t.Fatal("generated check shares nested command storage with preflight")
	}
}

func TestExpandConfigurationCheckPreservesExplicitInterval(t *testing.T) {
	tree := map[string]any{sectionPreflight: map[string]any{ServiceMonitorKeyConfig: map[string]any{
		checks.CheckKeyType: checks.CheckTypeCommand,
		EntryKeyInterval:    "1h",
	}}}

	if errs := expandConfigurationCheck(tree); len(errs) > 0 {
		t.Fatalf("expandConfigurationCheck: %v", errs)
	}
	entries := tree[sectionChecks].(map[string]any)
	got := entries[ConfigurationCheckName].(map[string]any)
	if got[EntryKeyInterval] != "1h" {
		t.Fatalf("generated interval = %v, want explicit 1h", got[EntryKeyInterval])
	}
}

func TestExpandConfigurationCheckSkipsMissingPreflight(t *testing.T) {
	tree := map[string]any{}
	if errs := expandConfigurationCheck(tree); len(errs) > 0 {
		t.Fatalf("expandConfigurationCheck: %v", errs)
	}
	if _, exists := tree[sectionChecks]; exists {
		t.Fatal("a service without preflight.config must not claim configuration health")
	}
}

func TestExpandConfigurationCheckRefusesToOverwrite(t *testing.T) {
	tree := map[string]any{
		sectionPreflight: map[string]any{ServiceMonitorKeyConfig: map[string]any{checks.CheckKeyType: checks.CheckTypeCommand}},
		sectionChecks:    map[string]any{ConfigurationCheckName: map[string]any{checks.CheckKeyType: checks.CheckTypeTCP}},
	}
	if errs := expandConfigurationCheck(tree); len(errs) == 0 {
		t.Fatal("want an error rather than overwriting the reserved check")
	}
}

func TestResolveExpandsAnalyzeForPreflightAndMonitoringCopy(t *testing.T) {
	cfg := &Config{
		Services: map[string]*Document{"web": {Body: map[string]any{
			ServiceKeyService: "web",
			sectionPreflight: map[string]any{ServiceMonitorKeyConfig: map[string]any{
				checks.CheckKeyType:    checks.CheckTypeCommand,
				checks.CheckKeyCommand: []any{"webctl", "configtest"},
				checks.CheckKeyAnalyze: map[string]any{keyAnalyzeUse: []any{"web"}},
			}},
		}}},
		Patterns: map[string]*Document{"web": {Body: map[string]any{
			rules.SectionRules: []any{map[string]any{"id": "syntax", "match": "syntax error", "severity": "error"}},
		}}},
	}

	resolved, errs := cfg.Resolve("web")
	if len(errs) > 0 {
		t.Fatalf("Resolve: %v", errs)
	}
	preflight := resolved.Tree[sectionPreflight].(map[string]any)[ServiceMonitorKeyConfig].(map[string]any)
	monitor := resolved.Tree[sectionChecks].(map[string]any)[ConfigurationCheckName].(map[string]any)
	for label, entry := range map[string]map[string]any{"preflight": preflight, "monitor": monitor} {
		analyze := entry[checks.CheckKeyAnalyze].(map[string]any)
		if gotRules, _ := analyze[rules.SectionRules].([]any); len(gotRules) != 1 {
			t.Fatalf("%s analyze = %#v, want one resolved rule", label, analyze)
		}
		if _, remains := analyze[keyAnalyzeUse]; remains {
			t.Fatalf("%s analyze kept catalog sugar: %#v", label, analyze)
		}
	}
}
