package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/cfgval"
)

// localOverrideFiles is the fixture every test here starts from: a catalog
// service with a metric watch, one configured service that uses it, and a host
// watch under a classified watches directory.
func localOverrideFiles() map[string]string {
	return map[string]string{
		"sermo.yml": `
engine: { backend: auto }
paths:
  services: [ "@ROOT@/services" ]
  apps: [ "@ROOT@/apps" ]
  notifiers: [ "@ROOT@/notifiers" ]
  watches: [ "@ROOT@/watches", "@ROOT@/storages" ]
  runtime: /run/sermo
defaults: { policy: { cooldown: 5m } }
`,
		"catalog/services/demo.yml": `
name: demo
service: demo
variables: { limit: "50000" }
watches:
  alert-if-fds-high:
    check:
      type: metric
      scope: service
      name: fds
      op: '>'
      value: ${limit}
      optional: true
    then: { action: alert, message: "fds high" }
  service:
    check: { type: service, expect: active }
`,
		"services/demo.yml": "name: demo\nuses: demo\n",
		"storages/root-free.yml": `
name: root-free
check: { type: storage, path: /, used_pct: { op: ">=", value: "90%" } }
`,
	}
}

func resolvedWatchCheck(t *testing.T, cfg *Config, service, watch string) map[string]any {
	t.Helper()
	resolved, errs := cfg.Resolve(service)
	if len(errs) != 0 {
		t.Fatalf("Resolve(%s) errors = %v", service, errs)
	}
	checks, _ := resolved.Tree[sectionChecks].(map[string]any)
	entry, ok := checks[watch].(map[string]any)
	if !ok {
		t.Fatalf("check %q missing from %v", watch, checks)
	}
	return entry
}

// TestLocalOverrideAdjustsServiceCheckThreshold is the headline case: a host
// raises one packaged threshold without touching the generated document, and
// every sibling field of that check survives.
func TestLocalOverrideAdjustsServiceCheckThreshold(t *testing.T) {
	files := localOverrideFiles()
	files["services.local/demo.yml"] = `
name: demo
watches:
  alert-if-fds-high:
    check:
      value: 200000
`
	cfg := loadCatalog(t, files)
	check := resolvedWatchCheck(t, cfg, "demo", "alert-if-fds-high")
	if got := cfgval.String(check["value"]); got != "200000" {
		t.Fatalf("threshold = %q, want the override's 200000", got)
	}
	for field, want := range map[string]string{"type": "metric", "scope": "service", "name": "fds", "op": ">"} {
		if got := cfgval.String(check[field]); got != want {
			t.Errorf("%s = %q, want the inherited %q", field, got, want)
		}
	}
}

// TestLocalOverrideDisablesInheritedWatch covers the other half of tuning: the
// host silences one packaged watch outright.
func TestLocalOverrideDisablesInheritedWatch(t *testing.T) {
	files := localOverrideFiles()
	files["services.local/demo.yml"] = "name: demo\nwatches:\n  alert-if-fds-high: { enabled: false }\n"
	cfg := loadCatalog(t, files)
	resolved, errs := cfg.Resolve("demo")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	checks, _ := resolved.Tree[sectionChecks].(map[string]any)
	if _, present := checks["alert-if-fds-high"]; present {
		t.Fatalf("disabled watch still generated a check: %v", checks)
	}
	if _, present := checks["service"]; !present {
		t.Fatalf("sibling watch was lost: %v", checks)
	}
}

// TestLocalOverrideIsTakenUnexpanded proves the most useful property: the
// override merges before ${var} expansion, so redefining one variable reaches
// every reference to it in the base document.
func TestLocalOverrideIsTakenUnexpanded(t *testing.T) {
	files := localOverrideFiles()
	files["services.local/demo.yml"] = "name: demo\nvariables: { limit: \"123456\" }\n"
	cfg := loadCatalog(t, files)
	check := resolvedWatchCheck(t, cfg, "demo", "alert-if-fds-high")
	if got := cfgval.String(check["value"]); got != "123456" {
		t.Fatalf("threshold = %q, want the redefined variable to reach ${limit}", got)
	}
}

// TestLocalOverrideRetunesHostWatch covers a watch document, which before this
// change was a hard load error rather than an override.
func TestLocalOverrideRetunesHostWatch(t *testing.T) {
	files := localOverrideFiles()
	files["storages.local/root-free.yml"] = `
name: root-free
check: { used_pct: { value: "70%" } }
interval: 5m
`
	cfg := loadCatalog(t, files)
	watches, _ := cfg.Global.Raw[pathKeyWatches].(map[string]any)
	entry, ok := watches["root-free"].(map[string]any)
	if !ok {
		t.Fatalf("watch missing from %v", watches)
	}
	if got := cfgval.String(entry["interval"]); got != "5m" {
		t.Errorf("interval = %q, want the override's 5m", got)
	}
	check, _ := entry["check"].(map[string]any)
	usedPct, _ := check["used_pct"].(map[string]any)
	if got := cfgval.String(usedPct["value"]); got != "70%" {
		t.Errorf("used_pct.value = %q, want the override's 70%%", got)
	}
	if got := cfgval.String(usedPct["op"]); got != ">=" {
		t.Errorf("used_pct.op = %q, want the inherited >=", got)
	}
	if got := cfgval.String(check["type"]); got != "storage" {
		t.Errorf("check type = %q, want the inherited storage", got)
	}
}

// TestLocalOverrideWithoutBaseLoadsAsDocument keeps the rule single: an override
// with no base is an ordinary document, so a host may add its own watch beside
// the generated ones.
func TestLocalOverrideWithoutBaseLoadsAsDocument(t *testing.T) {
	files := localOverrideFiles()
	files["watches.local/host-only.yml"] = `
name: host-only
check: { type: storage, path: /var, used_pct: { op: ">=", value: "80%" } }
`
	files["services.local/extra.yml"] = "name: extra\nuses: demo\n"
	cfg := loadCatalog(t, files)
	watches, _ := cfg.Global.Raw[pathKeyWatches].(map[string]any)
	if _, present := watches["host-only"]; !present {
		t.Fatalf("orphan watch override was not loaded: %v", watches)
	}
	if _, present := cfg.Services["extra"]; !present {
		t.Fatalf("orphan service override was not loaded: %v", cfg.ServiceNames)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate() issues = %v", issues)
	}
}

// TestLocalOverrideDoesNotCountAsDuplicate guards the merge path against the
// duplicate-name diagnostic that a second same-named document would otherwise
// produce.
func TestLocalOverrideDoesNotCountAsDuplicate(t *testing.T) {
	files := localOverrideFiles()
	files["services.local/demo.yml"] = "name: demo\nwatches:\n  alert-if-fds-high: { enabled: false }\n"
	cfg := loadCatalog(t, files)
	for _, issue := range Validate(cfg) {
		if strings.Contains(issue.Msg, "duplicate") {
			t.Fatalf("override reported as a duplicate: %+v", issue)
		}
	}
}

// TestLocalOverrideNamesTheFileInDiagnostics keeps a finding in a merged
// document traceable to the file the operator edited.
func TestLocalOverrideNamesTheFileInDiagnostics(t *testing.T) {
	files := localOverrideFiles()
	files["services.local/demo.yml"] = `
name: demo
watches:
  alert-if-fds-high:
    enable_if: { file: /etc/demo.conf }
`
	cfg := loadCatalog(t, files)
	issues := Validate(cfg)
	var scopes []string
	for _, issue := range issues {
		scopes = append(scopes, fmt.Sprintf("%s | %s", issue.Scope, issue.Msg))
		if strings.Contains(issue.Scope, filepath.Join("services.local", "demo.yml")) {
			return
		}
	}
	t.Fatalf("no issue named the override file; scopes = %v", scopes)
}

// TestLocalOverrideRejectsSecondOverrideForOneName keeps the layer unambiguous:
// the four classified watch directories share one namespace.
func TestLocalOverrideRejectsSecondOverrideForOneName(t *testing.T) {
	files := localOverrideFiles()
	body := "name: root-free\ncheck: { used_pct: { value: \"70%\" } }\n"
	files["storages.local/root-free.yml"] = body
	files["watches.local/root-free.yml"] = body
	_, err := loadConfig(t, writeConfig(t, files))
	if err == nil || !strings.Contains(err.Error(), "already overridden") {
		t.Fatalf("Load() error = %v, want an already-overridden failure", err)
	}
}

// TestLocalOverrideDirectoryInPathsIsRejected stops the layer being registered
// twice, which would load it as a base directory whose duplicates are fatal.
func TestLocalOverrideDirectoryInPathsIsRejected(t *testing.T) {
	files := localOverrideFiles()
	files["sermo.yml"] = strings.Replace(
		files["sermo.yml"],
		`services: [ "@ROOT@/services" ]`,
		`services: [ "@ROOT@/services", "@ROOT@/services.local" ]`, 1)
	files["services.local/demo.yml"] = "name: demo\nuses: demo\n"
	cfg, err := loadConfig(t, writeConfig(t, files))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, issue := range Validate(cfg) {
		if strings.Contains(issue.Msg, "per-host override directory") {
			return
		}
	}
	t.Fatalf("listing services.local in paths was accepted")
}

// TestLocalOverrideDerivedFromTrailingSlashPath pins the filepath.Clean: without
// it the sibling would be a hidden directory inside the base one.
func TestLocalOverrideDerivedFromTrailingSlashPath(t *testing.T) {
	files := localOverrideFiles()
	files["sermo.yml"] = strings.Replace(
		files["sermo.yml"], `services: [ "@ROOT@/services" ]`, `services: [ "@ROOT@/services/" ]`, 1)
	files["services.local/demo.yml"] = "name: demo\nwatches:\n  alert-if-fds-high: { enabled: false }\n"
	cfg := loadCatalog(t, files)
	resolved, errs := cfg.Resolve("demo")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	checks, _ := resolved.Tree[sectionChecks].(map[string]any)
	if _, present := checks["alert-if-fds-high"]; present {
		t.Fatalf("a trailing slash in paths.services lost the override: %v", checks)
	}
}

// TestLocalOverrideBeatsOSSelectorBranch is one of the two ordering
// regressions: an `os:` branch merges on top of its surrounding map, so an
// override folded in before the collapse would silently lose to it.
func TestLocalOverrideBeatsOSSelectorBranch(t *testing.T) {
	files := localOverrideFiles()
	files["catalog/services/demo.yml"] = `
name: demo
service: demo
os:
  ` + detectedOS + `:
    variables: { limit: "999" }
  default:
    variables: { limit: "50000" }
watches:
  alert-if-fds-high:
    check:
      type: metric
      scope: service
      name: fds
      op: '>'
      value: ${limit}
      optional: true
    then: { action: alert, message: "fds high" }
`
	files["services.local/demo.yml"] = "name: demo\nvariables: { limit: \"424242\" }\n"
	cfg := loadCatalog(t, files)
	check := resolvedWatchCheck(t, cfg, "demo", "alert-if-fds-high")
	if got := cfgval.String(check["value"]); got != "424242" {
		t.Fatalf("threshold = %q, want the override to beat the os: branch", got)
	}
}
