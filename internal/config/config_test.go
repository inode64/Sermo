package config

import (
	"fmt"
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

func TestMultiInstanceProfileOverridesPerInstance(t *testing.T) {
	// Two services share one profile (same binary, checks and rules) but each
	// overrides only the variables that make an instance unique: listen port,
	// pidfile and config path. This is the supported pattern for running e.g.
	// two MariaDB or php-fpm instances off a single profile — no new mechanism
	// is needed beyond `uses` + per-instance `variables`.
	cfg, err := Load(writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"profiles/dbserver.yml": `
kind: profile
name: dbserver
service:
  systemd: [dbserver]
variables:
  host: 127.0.0.1
  port: 3306
  pidfile: /run/dbserver/main.pid
  config: /etc/dbserver/main.cnf
processes:
  pidfile:
    type: pidfile
    path: "${pidfile}"
checks:
  tcp:
    type: tcp
    host: "${host}"
    port: "${port}"
  config:
    type: command
    command: ["dbserverd", "--defaults-file=${config}", "--help"]
`,
		"enabled/db-inst1.yml": `
kind: service
name: db-inst1
uses: dbserver
service: db-inst1
variables:
  port: 3306
  pidfile: /run/dbserver/inst1.pid
  config: /etc/dbserver/inst1.cnf
`,
		"enabled/db-inst2.yml": `
kind: service
name: db-inst2
uses: dbserver
service: db-inst2
variables:
  port: 3307
  pidfile: /run/dbserver/inst2.pid
  config: /etc/dbserver/inst2.cnf
`,
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	type want struct{ port, pidfile, config string }
	cases := map[string]want{
		"db-inst1": {port: "3306", pidfile: "/run/dbserver/inst1.pid", config: "/etc/dbserver/inst1.cnf"},
		"db-inst2": {port: "3307", pidfile: "/run/dbserver/inst2.pid", config: "/etc/dbserver/inst2.cnf"},
	}
	for name, w := range cases {
		resolved, errs := cfg.Resolve(name)
		if len(errs) != 0 {
			t.Fatalf("Resolve(%s) errors = %v", name, errs)
		}
		if got := scalarString(nested(t, resolved.Tree, "checks", "tcp")["port"]); got != w.port {
			t.Errorf("%s tcp.port = %q, want %q", name, got, w.port)
		}
		if got := scalarString(nested(t, resolved.Tree, "processes", "pidfile")["path"]); got != w.pidfile {
			t.Errorf("%s pidfile.path = %q, want %q", name, got, w.pidfile)
		}
		cmd, _ := nested(t, resolved.Tree, "checks", "config")["command"].([]any)
		if joined := fmt.Sprint(cmd...); !strings.Contains(joined, w.config) {
			t.Errorf("%s config check command = %v, want to contain %q", name, cmd, w.config)
		}
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

func TestLoadResolvesRelativePaths(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "configs")
	enabledDir := filepath.Join(configDir, "apps-enabled")
	profilesDir := filepath.Join(root, "profiles")
	for _, d := range []string{enabledDir, profilesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "redis.yml"), []byte(`
kind: profile
name: redis
variables: { port: 6379 }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(enabledDir, "redis-main.yml"), []byte(`
kind: service
name: redis-main
uses: redis
`), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(configDir, "sermo.yml")
	if err := os.WriteFile(global, []byte(`
engine: { backend: auto }
paths:
  profiles: [../profiles]
  enabled: [apps-enabled]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
watches:
  disk:
    enabled: false
    check: { type: disk, path: /, used_pct: { op: ">=", value: 90 } }
    then:
      hook: { command: [/bin/true] }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.Global.Enabled[0]; got != enabledDir {
		t.Fatalf("Enabled[0] = %q, want %q", got, enabledDir)
	}
	if got := cfg.Global.Profiles[0]; got != profilesDir {
		t.Fatalf("Profiles[0] = %q, want %q", got, profilesDir)
	}
	if len(cfg.Services) != 1 {
		t.Fatalf("Services = %d, want 1", len(cfg.Services))
	}
	watches, _ := cfg.Global.Raw["watches"].(map[string]any)
	if len(watches) != 1 {
		t.Fatalf("watches in global config = %d, want 1", len(watches))
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

func TestValidateWebBlock(t *testing.T) {
	goodGlobal := writeConfig(t, map[string]string{"sermo.yml": `
web: { address: 127.0.0.1, port: 9797 }
paths: { enabled: [ @ROOT@/enabled ] }
defaults: { policy: { cooldown: 5m } }
`})
	cfg, err := Load(goodGlobal)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range Validate(cfg) {
		if strings.Contains(i.Msg, "web.") {
			t.Fatalf("valid web block flagged: %v", i)
		}
	}

	badGlobal := writeConfig(t, map[string]string{"sermo.yml": `
web: { port: 70000, address: 5 }
paths: { enabled: [ @ROOT@/enabled ] }
defaults: { policy: { cooldown: 5m } }
`})
	cfg, err = Load(badGlobal)
	if err != nil {
		t.Fatal(err)
	}
	issues := Validate(cfg)
	if !hasIssue(issues, "web.port must be an integer in 1..65535") {
		t.Fatalf("missing web.port issue in %v", issues)
	}
	if !hasIssue(issues, "web.address must be a string") {
		t.Fatalf("missing web.address issue in %v", issues)
	}

	disabledGlobal := writeConfig(t, map[string]string{"sermo.yml": `
web: { address: 127.0.0.1 }
paths: { enabled: [ @ROOT@/enabled ] }
defaults: { policy: { cooldown: 5m } }
`})
	cfg, err = Load(disabledGlobal)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range Validate(cfg) {
		if strings.Contains(i.Msg, "web.") {
			t.Fatalf("web block without port should validate: %v", i)
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

func TestValidateRejectsPathLikeDocumentName(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/bad.yml": `
kind: service
name: ../escape
service: { name: mysql }
`,
		"profiles/bad.yml": `
kind: profile
name: apache/main
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	if !hasIssue(issues, `document name "../escape" must be a simple name without path separators`) {
		t.Fatalf("missing service name issue in %v", issues)
	}
	if !hasIssue(issues, `document name "apache/main" must be a simple name without path separators`) {
		t.Fatalf("missing profile name issue in %v", issues)
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

func TestBuiltinHostServiceAndRuntimeVars(t *testing.T) {
	old := detectedHost
	detectedHost = "myhost"
	defer func() { detectedHost = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/web.yml": `
kind: service
name: web
service: { name: nginx }
checks:
  ping:
    type: tcp
    host: "${host}"
    port: "80"
rules:
  alert-down:
    type: alert
    if: { failed: { check: ping } }
    then:
      action: alert
      message: "${service} on ${host}: ${event}/${action} at ${date}"
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("web")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v (runtime vars must not error)", errs)
	}
	// ${host} falls back to the hostname (no user-defined host variable).
	if got := scalarString(nested(t, resolved.Tree, "checks", "ping")["host"]); got != "myhost" {
		t.Errorf("ping host = %q, want myhost", got)
	}
	// ${service} → the backend unit name; ${host} resolved; runtime vars deferred.
	msg := scalarString(nested(t, resolved.Tree, "rules", "alert-down", "then")["message"])
	if !strings.Contains(msg, "nginx on myhost") {
		t.Errorf("message = %q, want service/host substituted", msg)
	}
	for _, lit := range []string{"${event}", "${action}", "${date}"} {
		if !strings.Contains(msg, lit) {
			t.Errorf("message = %q, want %s left for runtime", msg, lit)
		}
	}
}

func TestUserHostVariableOverridesBuiltin(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/web.yml": `
kind: service
name: web
service: { name: web }
variables:
  host: 127.0.0.1
checks:
  ping: { type: tcp, host: "${host}", port: "80" }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, _ := cfg.Resolve("web")
	if got := scalarString(nested(t, resolved.Tree, "checks", "ping")["host"]); got != "127.0.0.1" {
		t.Errorf("ping host = %q, want user-defined 127.0.0.1", got)
	}
}

func TestBuiltinPortVariable(t *testing.T) {
	// A top-level `port:` field feeds the built-in ${port}.
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/db.yml": `
kind: service
name: db
service: { name: db }
port: 6379
checks:
  ping: { type: tcp, host: "127.0.0.1", port: "${port}" }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("db")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if got := scalarString(nested(t, resolved.Tree, "checks", "ping")["port"]); got != "6379" {
		t.Errorf("ping port = %q, want 6379 (from top-level port)", got)
	}
}

func TestUserPortVariableOverridesBuiltin(t *testing.T) {
	// An explicit variables.port wins over the top-level `port:` field.
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/db.yml": `
kind: service
name: db
service: { name: db }
port: 6379
variables: { port: 7000 }
checks:
  ping: { type: tcp, host: "127.0.0.1", port: "${port}" }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, _ := cfg.Resolve("db")
	if got := scalarString(nested(t, resolved.Tree, "checks", "ping")["port"]); got != "7000" {
		t.Errorf("ping port = %q, want user-defined 7000", got)
	}
}

func TestUndefinedPortVariableErrors(t *testing.T) {
	// With neither a top-level port nor a variables.port, ${port} is undefined.
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/db.yml": `
kind: service
name: db
service: { name: db }
checks:
  ping: { type: tcp, host: "127.0.0.1", port: "${port}" }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, errs := cfg.Resolve("db"); len(errs) == 0 {
		t.Fatal("a ${port} with no port defined must error")
	}
}

func TestOSSelectorCollapses(t *testing.T) {
	old := detectedOS
	detectedOS = "gentoo"
	defer func() { detectedOS = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"profiles/apache.yml": `
kind: profile
name: apache
service:
  os:
    gentoo:
      systemd: [apache.service]
      openrc: [apache]
    debian:
      systemd: [apache2.service]
      openrc: [apache2]
checks:
  http:
    type: http
    timeout: 5s
    os:
      gentoo: { url: "http://localhost/gentoo" }
      debian: { url: "http://localhost/debian" }
policy:
  os:
    debian: { cooldown: 1m }
    default: { cooldown: 9m }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	body := cfg.Profiles["apache"].Body

	// service: the os: block is replaced by the gentoo branch.
	svc := body["service"].(map[string]any)
	if _, present := svc["os"]; present {
		t.Errorf("os selector not collapsed: %v", svc)
	}
	if sysd, _ := svc["systemd"].([]any); len(sysd) != 1 || sysd[0] != "apache.service" {
		t.Errorf("service.systemd = %v, want [apache.service]", svc["systemd"])
	}

	// checks.http: branch merged with its siblings (timeout kept, url added).
	http := nested(t, body, "checks", "http")
	if scalarString(http["timeout"]) != "5s" || scalarString(http["url"]) != "http://localhost/gentoo" {
		t.Errorf("checks.http = %v, want timeout 5s + gentoo url", http)
	}

	// policy: gentoo absent → the default branch applies.
	policy := body["policy"].(map[string]any)
	if scalarString(policy["cooldown"]) != "9m" {
		t.Errorf("policy.cooldown = %v, want default 9m", policy["cooldown"])
	}
}

func TestOSVariableBaked(t *testing.T) {
	old := detectedOS
	detectedOS = "debian"
	defer func() { detectedOS = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"profiles/app.yml": `
kind: profile
name: app
variables:
  binary: "/opt/${os}/bin/app"
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := profileBinary(cfg.Profiles["app"].Body); got != "/opt/debian/bin/app" {
		t.Errorf("baked binary = %q, want /opt/debian/bin/app", got)
	}
}

func TestDetectOSFromEnv(t *testing.T) {
	t.Setenv("SERMO_OS", "Gentoo")
	if got := detectOS(); got != "gentoo" {
		t.Errorf("detectOS() = %q, want gentoo", got)
	}
}

func TestArchVariableBaked(t *testing.T) {
	old := detectedArch
	detectedArch = "aarch64"
	defer func() { detectedArch = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"profiles/apps/qemu.yml": `
kind: profile
name: qemu
display_name: "QEMU"
variables:
  binary: "/usr/bin/qemu-system-${arch}"
preflight:
  binary: { type: binary, path: "${binary}" }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// ${arch} is baked into the variable value (so the no-nested-variables rule
	// never sees it) and flows through expansion.
	if got := profileBinary(cfg.Profiles["qemu"].Body); got != "/usr/bin/qemu-system-aarch64" {
		t.Errorf("baked binary = %q, want /usr/bin/qemu-system-aarch64", got)
	}
	resolved, errs := cfg.ResolveProfile("qemu")
	if len(errs) != 0 {
		t.Fatalf("ResolveProfile() errors = %v", errs)
	}
	bin := nested(t, resolved.Tree, "preflight", "binary")
	if scalarString(bin["path"]) != "/usr/bin/qemu-system-aarch64" {
		t.Errorf("resolved binary path = %v, want /usr/bin/qemu-system-aarch64", bin["path"])
	}
}

func TestProfileCategoryFromDirectory(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml":               baseGlobal,
		"profiles/nginx.yml":      "kind: profile\nname: nginx\nservice: { name: nginx }\n",
		"profiles/apps/git.yml":   "kind: profile\nname: git\nservice: { name: git }\n",
		"profiles/libs/glibc.yml": "kind: profile\nname: glibc\nvariables: { binary: /lib64/libc.so.6 }\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cases := map[string]string{"nginx": CategoryService, "git": CategoryApp, "glibc": CategoryLibrary}
	for name, want := range cases {
		doc, ok := cfg.Profiles[name]
		if !ok {
			t.Fatalf("profile %q not loaded", name)
		}
		if doc.Category != want {
			t.Errorf("%s category = %q, want %q", name, doc.Category, want)
		}
	}
	if got := cfg.ProfilesInCategory(CategoryApp); len(got) != 1 || got[0] != "git" {
		t.Errorf("ProfilesInCategory(app) = %v, want [git]", got)
	}
}

func TestRestartOnChangeDesugarsToChangedRule(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"profiles/libs/glibc.yml": `
kind: profile
name: glibc
display_name: "GNU C Library"
variables:
  binary: "/lib64/libc.so.6"
`,
		"enabled/web.yml": `
kind: service
name: web
service: { name: web }
restart_on_change:
  libraries: [glibc]
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("web")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if _, present := resolved.Tree["restart_on_change"]; present {
		t.Errorf("restart_on_change should be desugared away")
	}
	then := nested(t, resolved.Tree, "rules", "restart-on-change-glibc", "then")
	if scalarString(then["action"]) != "restart" {
		t.Errorf("generated rule action = %v, want restart", then["action"])
	}
	changed := nested(t, resolved.Tree, "rules", "restart-on-change-glibc", "if", "changed")
	if scalarString(changed["path"]) != "/lib64/libc.so.6" {
		t.Errorf("changed.path = %v, want /lib64/libc.so.6", changed["path"])
	}
}

func TestRestartOnChangeUnknownLibraryErrors(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		// nginx is a service profile, not a library: referencing it must error.
		"profiles/nginx.yml": "kind: profile\nname: nginx\nservice: { name: nginx }\n",
		"enabled/web.yml": `
kind: service
name: web
service: { name: web }
restart_on_change:
  libraries: [nginx, ghost]
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, errs := cfg.Resolve("web")
	joined := strings.Join(errs, "\n")
	for _, want := range []string{"nginx", "ghost"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected error mentioning %q, got %v", want, errs)
		}
	}
}

func TestDiscoverVersions(t *testing.T) {
	vtok := *tokenFor("x%v")
	ntok := *tokenFor("x%n")
	root := t.TempDir()
	for _, v := range []string{"7.4", "8.3", "12.0.2"} {
		dir := filepath.Join(root, "pkg-"+v, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "app"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A decoy that does not match the template's surrounding literals.
	if err := os.MkdirAll(filepath.Join(root, "other", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := discoverVersions(root+"/pkg-${version}/bin/app", vtok)
	want := []string{"12.0.2", "7.4", "8.3"} // sorted lexicographically
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("discoverVersions = %v, want %v", got, want)
	}

	if v := discoverVersions(root+"/pkg-${version}/bin/missing", vtok); len(v) != 0 {
		t.Errorf("no matches expected, got %v", v)
	}

	// Version embedded mid-filename, wrapped by literals on both sides (the
	// Berkeley DB db%vsql shape: /usr/bin/db4.8sql).
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"db4.8sql", "db6.0sql", "dbsql" /* no version, must be ignored */} {
		if err := os.WriteFile(filepath.Join(bin, f), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got = discoverVersions(bin+"/db${version}sql", vtok)
	want = []string{"4.8", "6.0"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("mid-filename discoverVersions = %v, want %v", got, want)
	}

	// Version at the end of the name (unbounded on the right): only digit-leading
	// matches count, so a bare binary and a stray .conf are not mistaken for a
	// version.
	sbin := filepath.Join(root, "sbin")
	if err := os.MkdirAll(sbin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"php-fpm8.3", "php-fpm7.4", "php-fpm" /* generic */, "php-fpm.conf" /* decoy */} {
		if err := os.WriteFile(filepath.Join(sbin, f), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got = discoverVersions(sbin+"/php-fpm${version}", vtok)
	want = []string{"7.4", "8.3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("trailing-version discoverVersions = %v, want %v", got, want)
	}

	// %n (${n}) accepts only whole integers: python2/python3 match, but
	// python3.11 and python-config do not.
	pbin := filepath.Join(root, "usrbin")
	if err := os.MkdirAll(pbin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"python2", "python3", "python3.11", "python-config"} {
		if err := os.WriteFile(filepath.Join(pbin, f), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got = discoverVersions(pbin+"/python${n}", ntok)
	want = []string{"2", "3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("integer discoverVersions = %v, want %v", got, want)
	}
}

// TestVersionTemplateDiscoverFrom covers a template whose monitored binary is
// generic (no ${version}); versions come from an explicit `versions.from` path,
// and ${version} is baked into aliases. The `versions` block must not leak into
// the materialized profile.
func TestVersionTemplateDiscoverFrom(t *testing.T) {
	root := t.TempDir()
	slots := filepath.Join(root, "lib")
	for _, v := range []string{"7.4", "8.3"} {
		dir := filepath.Join(slots, "php"+v, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "php-fpm"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	profilesDir := filepath.Join(root, "profiles")
	enabledDir := filepath.Join(root, "enabled")
	if err := os.MkdirAll(enabledDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tmpl := fmt.Sprintf(`
kind: profile
name: php-fpm%%v
display_name: "PHP-FPM ${version}"
service:
  systemd: ["php${version}-fpm"]
versions:
  from: "%s/php${version}/bin/php-fpm"
variables:
  binary: /usr/sbin/php-fpm
`, slots)
	if err := os.WriteFile(filepath.Join(profilesDir, "php-fpm%v.yml"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { profiles: [ %s ], enabled: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, profilesDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, v := range []string{"7.4", "8.3"} {
		doc, ok := cfg.Profiles["php-fpm"+v]
		if !ok {
			t.Fatalf("expected materialized profile php-fpm%s", v)
		}
		// Generic binary is preserved; version did not leak into it.
		if got := profileBinary(doc.Body); got != "/usr/sbin/php-fpm" {
			t.Errorf("php-fpm%s binary = %q, want /usr/sbin/php-fpm", v, got)
		}
		// ${version} baked into the service unit candidate.
		sysd := nested(t, doc.Body, "service")["systemd"].([]any)
		if got := sysd[0].(string); got != "php"+v+"-fpm" {
			t.Errorf("php-fpm%s service unit = %q, want php%s-fpm", v, got, v)
		}
		// Discovery metadata stripped from the concrete profile.
		if _, present := doc.Body["versions"]; present {
			t.Errorf("php-fpm%s still carries versions block", v)
		}
	}
}

// TestVersionTemplateMaterialization exercises a `name: foo-%v` template: it must
// produce one profile per installed version (with ${version} baked into binary
// and display_name), inherit a `uses` base, and drop the template itself.
func TestVersionTemplateMaterialization(t *testing.T) {
	root := t.TempDir()
	binRoot := filepath.Join(root, "opt")
	for _, v := range []string{"7.4", "8.3"} {
		dir := filepath.Join(binRoot, "php"+v, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "php-fpm"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	profilesDir := filepath.Join(root, "profiles")
	enabledDir := filepath.Join(root, "enabled")
	if err := os.MkdirAll(enabledDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(dir, file, content string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Rich base with a marker rule and an extra variable, to prove inheritance.
	write(profilesDir, "php-fpm.yml", `
kind: profile
name: php-fpm
display_name: "PHP-FPM"
service: { name: php-fpm }
variables:
  binary: /usr/sbin/php-fpm
  user: www-data
rules:
  block-bad-config:
    type: guard
    blocks: [restart]
    if:
      failed:
        check: config
    then:
      action: block
      message: "${display_name} configuration is invalid"
`)
	// Version template inheriting the base, overriding only the binary.
	write(profilesDir, "php-fpm-%v.yml", fmt.Sprintf(`
kind: profile
name: php-fpm-%%v
uses: php-fpm
display_name: "PHP-FPM ${version}"
variables:
  binary: "%s/php${version}/bin/php-fpm"
`, binRoot))

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths:
  profiles: [ %s ]
  enabled: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, profilesDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Template must be gone; one concrete profile per installed version present.
	if _, ok := cfg.Profiles["php-fpm-%v"]; ok {
		t.Errorf("template php-fpm-%%v should not be registered")
	}
	for _, v := range []string{"7.4", "8.3"} {
		name := "php-fpm-" + v
		doc, ok := cfg.Profiles[name]
		if !ok {
			t.Fatalf("expected materialized profile %q", name)
		}
		// display_name has the version baked in (no literal ${version}).
		if got := DisplayName(doc.Body, name); got != "PHP-FPM "+v {
			t.Errorf("%s display_name = %q, want %q", name, got, "PHP-FPM "+v)
		}
		// Inherited the base rule, and ${version} is baked into the binary path.
		wantBin := fmt.Sprintf("%s/php%s/bin/php-fpm", binRoot, v)
		if got := profileBinary(doc.Body); got != wantBin {
			t.Errorf("%s binary = %q, want %q", name, got, wantBin)
		}
		if _, ok := nested(t, doc.Body, "rules")["block-bad-config"]; !ok {
			t.Errorf("%s did not inherit base rule", name)
		}
	}

	// A service using a materialized version resolves end to end, including the
	// inherited rule message expanding through the baked display_name.
	write(enabledDir, "site.yml", `
kind: service
name: site
uses: php-fpm-8.3
service: { name: php-fpm }
`)
	cfg, err = Load(global)
	if err != nil {
		t.Fatalf("Load() reload error = %v", err)
	}
	resolved, errs := cfg.Resolve("site")
	if len(errs) != 0 {
		t.Fatalf("Resolve(site) errors = %v", errs)
	}
	then := nested(t, resolved.Tree, "rules", "block-bad-config", "then")
	if got := scalarString(then["message"]); got != "PHP-FPM 8.3 configuration is invalid" {
		t.Errorf("message = %q, want %q", got, "PHP-FPM 8.3 configuration is invalid")
	}
}
