package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

const baseGlobal = `
engine:
  backend: auto
paths:
  profiles: [ @ROOT@/profiles ]
  enabled: [ @ROOT@/enabled ]
  runtime: /run/sermo
defaults:
  policy:
    cooldown: 5m
  stop_policy:
    graceful_timeout: 30s
    force_kill: false
`

func TestResolveMergesDefaultsProfileOverrides(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"profiles/apache.yml": `
kind: profile
name: apache
variables:
  host: 127.0.0.1
  port: 8080
checks:
  http:
    type: http
    url: "http://${host}:${port}/health"
    expect_status: 200
policy:
  max_actions: 3
`,
		"enabled/apache-main.yml": `
kind: service
name: apache-main
uses: apache
checks:
  http:
    url: "http://${host}:${port}/"
`,
	})

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("apache-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}

	http := nested(t, resolved.Tree, "checks", "http")
	if got := http["url"]; got != "http://127.0.0.1:8080/" {
		t.Errorf("url = %v, want override expanded", got)
	}
	if got := scalarString(http["expect_status"]); got != "200" {
		t.Errorf("expect_status = %v, want inherited 200", got)
	}
	policy := nested(t, resolved.Tree, "policy")
	if got := scalarString(policy["cooldown"]); got != "5m" {
		t.Errorf("cooldown = %v, want default 5m", got)
	}
	if got := scalarString(policy["max_actions"]); got != "3" {
		t.Errorf("max_actions = %v, want profile 3", got)
	}
	stop := nested(t, resolved.Tree, "stop_policy")
	if got := scalarString(stop["graceful_timeout"]); got != "30s" {
		t.Errorf("graceful_timeout = %v, want default 30s", got)
	}
}

func TestCloneOverridesVariableBeforeExpansion(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/redis-main.yml": `
kind: service
name: redis-main
variables:
  port: 6379
checks:
  ping:
    type: tcp
    port: "${port}"
`,
		"enabled/redis-cache.yml": `
kind: service
name: redis-cache
clone: redis-main
variables:
  port: 6380
`,
	})

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("redis-cache")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	ping := nested(t, resolved.Tree, "checks", "ping")
	if got := scalarString(ping["port"]); got != "6380" {
		t.Errorf("cloned port = %v, want overridden 6380", got)
	}
}

func TestValidateCleanConfig(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/redis-main.yml": `
kind: service
name: redis-main
service: { name: redis }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate() issues = %v, want none", issues)
	}
}

func TestValidateGlobalErrors(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
engine:
  backend: bogus
paths:
  profiles: [ @ROOT@/profiles ]
  enabled: [ @ROOT@/enabled ]
  locks: /run/sermo/locks
  runtime: relative/path
defaults:
  policy:
    cooldown: 0s
security:
  allow_sigkill_by_default: true
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	wantSubstrings := []string{
		"engine.backend",
		"paths.locks",
		"paths.runtime",
		"security.allow_sigkill_by_default",
		"defaults.policy.cooldown",
	}
	for _, want := range wantSubstrings {
		if !hasIssue(issues, want) {
			t.Errorf("missing issue containing %q in %v", want, issues)
		}
	}
}

func TestValidateMissingVariableAndPort(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/bad.yml": `
kind: service
name: bad
checks:
  http: { type: http, url: "http://${missing}/", port: 99999 }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	if !hasIssue(issues, "variable ${missing} used in checks.http.url") {
		t.Errorf("missing undefined-variable issue: %v", issues)
	}
	if !hasIssue(issues, "must resolve to a port in 1..65535") {
		t.Errorf("missing port-range issue: %v", issues)
	}
}

func TestValidateCloneCycle(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/a.yml": `
kind: service
name: a
clone: b
`,
		"enabled/b.yml": `
kind: service
name: b
clone: a
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !hasIssue(Validate(cfg), "clone cycle detected") {
		t.Errorf("expected clone-cycle issue")
	}
}

func TestValidateNestedVariableRejected(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/nested.yml": `
kind: service
name: nested
variables:
  a: "${b}"
  b: "x"
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !hasIssue(Validate(cfg), "references another variable") {
		t.Errorf("expected nested-variable issue")
	}
}

func TestCollectVariablesFirstExistingPath(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "usr-lib-binary")
	if err := os.WriteFile(present, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "lib-binary")

	// First candidate is missing, second exists: resolves to the second.
	vars := collectVariables(map[string]any{
		"variables": map[string]any{
			"binary": []any{missing, present},
		},
	})
	if vars["binary"] != present {
		t.Errorf("binary = %q, want first existing %q", vars["binary"], present)
	}

	// Stops at the first hit even when a later candidate also exists.
	vars = collectVariables(map[string]any{
		"variables": map[string]any{
			"binary": []any{present, missing},
		},
	})
	if vars["binary"] != present {
		t.Errorf("binary = %q, want %q", vars["binary"], present)
	}

	// None exist: falls back to the first candidate so the value stays usable.
	other := filepath.Join(dir, "also-missing")
	vars = collectVariables(map[string]any{
		"variables": map[string]any{
			"binary": []any{missing, other},
		},
	})
	if vars["binary"] != missing {
		t.Errorf("binary = %q, want fallback to first %q", vars["binary"], missing)
	}
}

func TestBuiltinNameAndDisplayNameVariables(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"profiles/db.yml": `
kind: profile
name: db
display_name: "MariaDB"
rules:
  guard-backup:
    type: guard
    blocks: [restart]
    if:
      active:
        check: service
    then:
      action: block
      message: "${display_name} backup is running on ${name}"
`,
		// Inherits the profile's display_name; name is its own.
		"enabled/db-main.yml": `
kind: service
name: db-main
uses: db
service: { name: db }
`,
		// No display_name anywhere: ${display_name} must fall back to name.
		"enabled/plain.yml": `
kind: service
name: plain
service: { name: plain }
rules:
  alert-x:
    type: alert
    if:
      failed:
        check: service
    then:
      action: alert
      message: "${display_name} is down"
`,
		// Explicit variable overrides the built-in.
		"enabled/custom.yml": `
kind: service
name: custom
service: { name: custom }
variables:
  display_name: "Overridden"
rules:
  alert-y:
    type: alert
    if:
      failed:
        check: service
    then:
      action: alert
      message: "${display_name}"
`,
	})

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	check := func(service, rule string, want string) {
		t.Helper()
		resolved, errs := cfg.Resolve(service)
		if len(errs) != 0 {
			t.Fatalf("Resolve(%q) errors = %v", service, errs)
		}
		then := nested(t, resolved.Tree, "rules", rule, "then")
		if got := scalarString(then["message"]); got != want {
			t.Errorf("%s message = %q, want %q", service, got, want)
		}
	}

	check("db-main", "guard-backup", "MariaDB backup is running on db-main")
	check("plain", "alert-x", "plain is down")
	check("custom", "alert-y", "Overridden")
}

func TestDisplayNameFallsBackToName(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"present", map[string]any{"display_name": "MariaDB"}, "MariaDB"},
		{"absent", map[string]any{}, "mariadb"},
		{"blank", map[string]any{"display_name": "   "}, "mariadb"},
		{"non-string", map[string]any{"display_name": 7}, "mariadb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayName(tc.body, "mariadb"); got != tc.want {
				t.Errorf("DisplayName(%v) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// TestDescriptionHasNoFallback guards the asymmetry: unlike display_name,
// description is never materialized from name. A document without a description
// renders without one.
func TestDescriptionHasNoFallback(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/plain.yml": `
kind: service
name: plain
service: { name: plain }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("plain")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if _, present := resolved.Tree["description"]; present {
		t.Errorf("description should be absent, got %v", resolved.Tree["description"])
	}
}

func TestValidateDuplicateServiceName(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/one.yml": `
kind: service
name: dup
`,
		"enabled/two.yml": `
kind: service
name: dup
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !hasIssue(Validate(cfg), "duplicate service name") {
		t.Errorf("expected duplicate-name issue")
	}
}

func TestMergeMapsRecursive(t *testing.T) {
	dst := map[string]any{"policy": map[string]any{"max_actions": 3, "cooldown": "2m"}}
	src := map[string]any{"policy": map[string]any{"cooldown": "5m"}}
	out := mergeMaps(dst, src)

	policy := out["policy"].(map[string]any)
	if policy["cooldown"] != "5m" {
		t.Errorf("cooldown = %v, want 5m", policy["cooldown"])
	}
	if scalarString(policy["max_actions"]) != "3" {
		t.Errorf("max_actions = %v, want preserved 3", policy["max_actions"])
	}
	// Source must not be aliased into the result.
	src["policy"].(map[string]any)["cooldown"] = "9m"
	if out["policy"].(map[string]any)["cooldown"] != "5m" {
		t.Errorf("merge aliased the source map")
	}
}

func TestApplyDeletesRemovesEntry(t *testing.T) {
	tree := map[string]any{
		"checks": map[string]any{
			"http": map[string]any{"delete": true},
			"tcp":  map[string]any{"type": "tcp"},
		},
	}
	applyDeletes(tree)
	checks := tree["checks"].(map[string]any)
	if _, ok := checks["http"]; ok {
		t.Errorf("http should be deleted")
	}
	if _, ok := checks["tcp"]; !ok {
		t.Errorf("tcp should remain")
	}
}

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
