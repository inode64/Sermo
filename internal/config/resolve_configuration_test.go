package config

import (
	"strings"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/rules"
	"sermo/internal/severity"
)

// The monitoring copy is an error by default: while it fails, the required
// preflight refuses every operation, automatic recovery included.
func TestExpandConfigurationCheckCopiesConfigPreflightAsAnError(t *testing.T) {
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
	if got[checks.CheckKeyType] != checks.CheckTypeCommand || got[checks.CheckKeySeverity] != string(severity.Error) {
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

// A grade declared on preflight.config wins: an operator may demote a test
// whose failures are routine on that host.
func TestExpandConfigurationCheckKeepsADeclaredSeverity(t *testing.T) {
	tree := map[string]any{sectionPreflight: map[string]any{ServiceMonitorKeyConfig: map[string]any{
		checks.CheckKeyType:     checks.CheckTypeCommand,
		checks.CheckKeySeverity: string(severity.Warning),
	}}}
	if errs := expandConfigurationCheck(tree); len(errs) > 0 {
		t.Fatalf("expandConfigurationCheck: %v", errs)
	}
	got := tree[sectionChecks].(map[string]any)[ConfigurationCheckName].(map[string]any)
	if got[checks.CheckKeySeverity] != string(severity.Warning) {
		t.Fatalf("generated severity = %v, want the declared warning", got[checks.CheckKeySeverity])
	}
}

// Catalog profiles get the error grade without declaring it: named, whose
// unloadable zones went unreported for a year as a warning, and apache, whose
// os-selected command must not lose it.
func TestCatalogConfigurationCheckIsAnError(t *testing.T) {
	cfg := loadAllCatalogServices(t, repoCatalogDir(repoRoot(t)), "openrc")
	for _, name := range []string{"apache-audit", "named-audit"} {
		resolved, errs := cfg.Resolve(name)
		if len(errs) > 0 {
			t.Fatalf("resolve %s: %v", name, errs)
		}
		check := resolved.Tree[sectionChecks].(map[string]any)[ConfigurationCheckName].(map[string]any)
		if check[checks.CheckKeySeverity] != string(severity.Error) {
			t.Fatalf("%s configuration check severity = %v, want error", name, check[checks.CheckKeySeverity])
		}
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

func TestResolveRecordsAnalyzePatternSets(t *testing.T) {
	command := func(use ...any) map[string]any {
		return map[string]any{
			checks.CheckKeyType:    checks.CheckTypeCommand,
			checks.CheckKeyCommand: []any{"webctl", "configtest"},
			checks.CheckKeyAnalyze: map[string]any{keyAnalyzeUse: use},
		}
	}
	set := func(id string) *Document {
		return &Document{Body: map[string]any{
			rules.SectionRules: []any{map[string]any{"id": id, "match": id, "severity": "error"}},
		}}
	}
	cfg := &Config{
		Services: map[string]*Document{
			"web": {Body: map[string]any{
				ServiceKeyService: "web",
				sectionChecks:     map[string]any{"syntax": command("web", "common")},
				sectionPreflight:  map[string]any{"syntax": command("common")},
				sectionWatches:    map[string]any{"log": map[string]any{WatchKeyCheck: command("logs")}},
			}},
			"db": {Body: map[string]any{ServiceKeyService: "db"}},
		},
		Patterns: map[string]*Document{"web": set("w"), "common": set("c"), "logs": set("l")},
	}

	resolved, errs := cfg.Resolve("web")
	if len(errs) > 0 {
		t.Fatalf("Resolve: %v", errs)
	}
	if got, want := strings.Join(resolved.PatternSets, ","), "common,logs,web"; got != want {
		t.Fatalf("PatternSets = %q, want %q (sorted, unique, from checks, preflight and watch checks)", got, want)
	}
	if plain, errs := cfg.Resolve("db"); len(errs) > 0 || plain.PatternSets != nil {
		t.Fatalf("service without analyze: PatternSets = %#v, errs = %v", plain.PatternSets, errs)
	}
}
