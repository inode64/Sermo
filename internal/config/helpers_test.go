package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/cfgval"
)

// writeConfig lays out a temp config tree. files maps a relative path under the
// root to its YAML content; it returns the global config path.
func writeConfig(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	global := filepath.Join(root, "sermo.yml")
	if _, ok := files["sermo.yml"]; !ok {
		t.Fatalf("writeConfig requires a sermo.yml entry")
	}
	// Rewrite the global file with absolute path placeholders resolved.
	content := strings.ReplaceAll(files["sermo.yml"], "@ROOT@", root)
	if err := os.WriteFile(global, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return global
}

func loadConfig(t *testing.T, global string, opts ...Option) (*Config, error) {
	t.Helper()
	catalogDir := filepath.Join(filepath.Dir(global), "catalog")
	if _, err := os.Stat(catalogDir); err == nil {
		opts = append([]Option{WithCatalogDirs(catalogDir)}, opts...)
	}
	return Load(global, opts...)
}

func withPathDirs(kind string) Option {
	return func(o *loadOptions) {
		if o.pathDirs == nil {
			o.pathDirs = map[string][]string{}
		}
		o.pathDirs[kind] = nil
	}
}

func withServiceUnits(backend string, units []string) Option {
	return func(o *loadOptions) {
		if o.serviceUnits == nil {
			o.serviceUnits = map[string][]string{}
		}
		o.serviceUnits[backend] = normalizeServiceUnits(units)
	}
}

// loadCatalog loads a config from files, failing on any load error.
func loadCatalog(t *testing.T, files map[string]string) *Config {
	t.Helper()
	cfg, err := loadConfig(t, writeConfig(t, files))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return cfg
}

// assertValidateIssue loads a config from files and asserts Validate reports
// an issue containing want.
func assertValidateIssue(t *testing.T, files map[string]string, want string) {
	t.Helper()
	if !hasIssue(Validate(loadCatalog(t, files)), want) {
		t.Fatalf("Validate() did not report %q", want)
	}
}

// assertCatalogValidation loads a config from files and asserts Validate reports
// no issue scoped to goodScope, then asserts every want appears in some issue.
func assertCatalogValidation(t *testing.T, files map[string]string, goodScope string, want ...string) {
	t.Helper()
	issues := Validate(loadCatalog(t, files))
	for _, issue := range issues {
		if issue.Scope == goodScope {
			t.Fatalf("valid %s flagged: %v", goodScope, issues)
		}
	}
	for _, w := range want {
		mustHave(t, issues, w)
	}
}

// assertLoadError writes the given files and asserts Load fails with an error
// containing want.
func assertLoadError(t *testing.T, files map[string]string, want string) {
	t.Helper()
	if _, err := loadConfig(t, writeConfig(t, files)); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Load() error = %v, want %q", err, want)
	}
}

// resolveInstance loads a config from files and resolves instance, failing on
// any load or resolve error.
func resolveInstance(t *testing.T, files map[string]string, instance string) Resolved {
	t.Helper()
	resolved, errs := loadCatalog(t, files).Resolve(instance)
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	return resolved
}

// resolveValidInstance loads a config from files, asserts it reports no
// validation issues, then resolves instance (failing on resolve errors).
func resolveValidInstance(t *testing.T, files map[string]string, instance string) Resolved {
	t.Helper()
	cfg := loadCatalog(t, files)
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate() issues = %v, want none", issues)
	}
	resolved, errs := cfg.Resolve(instance)
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	return resolved
}

// assertResolvedCheckField loads a config from files, resolves instance, and
// asserts the given field of the named check resolved to want.
func assertResolvedCheckField(t *testing.T, files map[string]string, instance, check, field, want string) {
	t.Helper()
	resolved := resolveInstance(t, files, instance)
	if got := cfgval.String(nested(t, resolved.Tree, "checks", check)[field]); got != want {
		t.Errorf("%s.%s = %q, want %q", check, field, got, want)
	}
}

// assertLoadDirError writes a sermo.yml exposing the dirKey path plus one
// fragment file inside it, and asserts Load fails with an error containing want.
func assertLoadDirError(t *testing.T, dirKey, file, body, want string) {
	t.Helper()
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  ` + dirKey + `: [ @ROOT@/` + dirKey + ` ]
defaults:
  policy: { cooldown: 5m }
`,
		file: body,
	})
	if _, err := loadConfig(t, global); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Load() error = %v, want %q", err, want)
	}
}

// writeFile writes content to dir/file, creating dir if needed.
func writeFile(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeBinDir creates a fresh <tmp>/bin directory holding each named executable
// (content "x", mode 0o755) and returns its path.
func makeBinDir(t *testing.T, names ...string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

// makeVersionedBinaries creates, for each version, a root/<prefix><version>/bin
// directory holding an executable named binary (content "x", mode 0o755).
func makeVersionedBinaries(t *testing.T, root, prefix, binary string, versions ...string) {
	t.Helper()
	for _, v := range versions {
		dir := filepath.Join(root, prefix+v, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, binary), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// assertExpandedVar asserts a catalog variable referenced from the top-level
// convenience key (socket, lockfile, ...) expands into the derived check path.
func assertExpandedVar(t *testing.T, key, value string) {
	t.Helper()
	assertResolvedCheckField(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": "name: svc\nvariables:\n  " + key + ": " + value + "\n" +
			key + ": \"${" + key + "}\"\nchecks:\n  service: { type: service, expect: active }\n",
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	}, "svc-main", key, "path", value)
}

const baseGlobal = `
engine:
  backend: auto
paths:
  services: [ @ROOT@/services ]
  runtime: /run/sermo
defaults:
  policy:
    cooldown: 5m
  stop_policy:
    graceful_timeout: 30s
    force_kill: false
`

func nested(t *testing.T, tree map[string]any, keys ...string) map[string]any {
	t.Helper()
	cur := tree
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			t.Fatalf("path %v: key %q is not a map (tree=%v)", keys, k, tree)
		}
		cur = next
	}
	return cur
}

func hasIssue(issues []Issue, substr string) bool {
	for _, is := range issues {
		if strings.Contains(is.Msg, substr) {
			return true
		}
	}
	return false
}

func idOf(r any) string { return r.(map[string]any)["id"].(string) }

func hasSub(errs []string, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

// validateService builds a single-service config (merged onto baseGlobal) and
// returns the issues for that service.
func validateService(t *testing.T, serviceYAML string) []Issue {
	t.Helper()
	global := writeConfig(t, map[string]string{
		"sermo.yml":        baseGlobal,
		"services/svc.yml": serviceYAML,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return Validate(cfg)
}

// validateGlobalDoc loads a config from a single sermo.yml document and returns
// its validation issues.
func validateGlobalDoc(t *testing.T, sermoYAML string) []Issue {
	t.Helper()
	cfg, err := loadConfig(t, writeConfig(t, map[string]string{"sermo.yml": sermoYAML}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return Validate(cfg)
}

func mustHave(t *testing.T, issues []Issue, substr string) {
	t.Helper()
	if !hasIssue(issues, substr) {
		t.Fatalf("missing issue %q in %v", substr, issues)
	}
}

// mustNotHave fails if any issue message contains substr.
func mustNotHave(t *testing.T, issues []Issue, substr string) {
	t.Helper()
	for _, is := range issues {
		if strings.Contains(is.Msg, substr) {
			t.Fatalf("unexpected issue %q in %v", substr, issues)
		}
	}
}

// assertServiceValidationTokens validates goodYAML and asserts none of its
// issues mention any goodToken, then validates badYAML and asserts every want
// appears in some issue.
func assertServiceValidationTokens(t *testing.T, goodYAML string, goodTokens []string, badYAML string, want ...string) {
	t.Helper()
	good := validateService(t, goodYAML)
	for _, tok := range goodTokens {
		mustNotHave(t, good, tok)
	}
	bad := validateService(t, badYAML)
	for _, w := range want {
		mustHave(t, bad, w)
	}
}

// assertServiceValidation validates goodYAML and asserts none of its issues
// mention goodToken, then validates badYAML and asserts every want appears in
// some issue.
func assertServiceValidation(t *testing.T, goodYAML, goodToken, badYAML string, want ...string) {
	t.Helper()
	assertServiceValidationTokens(t, goodYAML, []string{goodToken}, badYAML, want...)
}

// serviceIssueCase is one service-document validation case: load service and
// assert which issue substrings Validate must report (want) and must not
// (absent). A case can use either or both.
type serviceIssueCase struct {
	name    string
	service string
	want    []string
	absent  []string
}

func runServiceIssueCases(t *testing.T, tests []serviceIssueCase) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := validateService(t, tt.service)
			for _, want := range tt.want {
				mustHave(t, issues, want)
			}
			for _, absent := range tt.absent {
				mustNotHave(t, issues, absent)
			}
		})
	}
}
