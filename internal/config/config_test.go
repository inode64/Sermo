package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sermo/internal/cfgval"
	"slices"
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
  catalog: [ @ROOT@/catalog ]
  includes: [ @ROOT@/enabled ]
  runtime: /run/sermo
defaults:
  policy:
    cooldown: 5m
  stop_policy:
    graceful_timeout: 30s
    force_kill: false
`

func TestResolveMergesDefaultsDaemonOverrides(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apache.yml": `
kind: daemon
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
	if got := cfgval.String(http["expect_status"]); got != "200" {
		t.Errorf("expect_status = %v, want inherited 200", got)
	}
	policy := nested(t, resolved.Tree, "policy")
	if got := cfgval.String(policy["cooldown"]); got != "5m" {
		t.Errorf("cooldown = %v, want default 5m", got)
	}
	if got := cfgval.String(policy["max_actions"]); got != "3" {
		t.Errorf("max_actions = %v, want daemon 3", got)
	}
	stop := nested(t, resolved.Tree, "stop_policy")
	if got := cfgval.String(stop["graceful_timeout"]); got != "30s" {
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
	if got := cfgval.String(ping["port"]); got != "6380" {
		t.Errorf("cloned port = %v, want overridden 6380", got)
	}
}

func TestMultiInstanceDaemonOverridesPerInstance(t *testing.T) {
	// Two services share one daemon (same binary, checks and rules) but each
	// overrides only the variables that make an instance unique: listen port,
	// pidfile and config path. This is the supported pattern for running e.g.
	// two MariaDB or php-fpm instances off a single daemon — no new mechanism
	// is needed beyond `uses` + per-instance `variables`.
	cfg, err := Load(writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/dbserver.yml": `
kind: daemon
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
		if got := cfgval.String(nested(t, resolved.Tree, "checks", "tcp")["port"]); got != w.port {
			t.Errorf("%s tcp.port = %q, want %q", name, got, w.port)
		}
		if got := cfgval.String(nested(t, resolved.Tree, "processes", "pidfile")["path"]); got != w.pidfile {
			t.Errorf("%s pidfile.path = %q, want %q", name, got, w.pidfile)
		}
		cmd, _ := nested(t, resolved.Tree, "checks", "config")["command"].([]any)
		if joined := fmt.Sprint(cmd...); !strings.Contains(joined, w.config) {
			t.Errorf("%s config check command = %v, want to contain %q", name, cmd, w.config)
		}
	}
}

func TestAppsLinkInjectsAppPreflight(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/java.yml": `
kind: daemon
name: java
binary: /usr/bin/java
preflight:
  binary: { type: binary, path: "${binary}" }
  health: { type: command, command: ["${binary}", "-help"] }
  version: { type: command, command: ["${binary}", "-version"] }
`,
		"catalog/tomcat.yml": `
kind: daemon
name: tomcat
apps: [java]
binary: /opt/tomcat/bin/catalina.sh
variables: { port: 8080 }
preflight:
  binary: { type: binary, path: "${binary}" }
checks:
  port: { type: tcp, port: "${port}" }
`,
		"enabled/tomcat-main.yml": `
kind: service
name: tomcat-main
uses: tomcat
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("tomcat-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	pf := nested(t, resolved.Tree, "preflight")
	// The linked app's checks are injected namespaced; the service's own stay.
	if _, ok := pf["binary"]; !ok {
		t.Errorf("service's own preflight binary missing")
	}
	jbin, ok := pf["java-binary"].(map[string]any)
	if !ok {
		t.Fatalf("java-binary not injected: %v", pf)
	}
	// It carries java's binary path (expanded with java's vars), not tomcat's.
	if got := cfgval.String(jbin["path"]); got != "/usr/bin/java" {
		t.Errorf("java-binary path = %q, want /usr/bin/java", got)
	}
	if _, ok := pf["java-version"]; !ok {
		t.Errorf("java-version not injected: %v", pf)
	}
	if _, ok := pf["java-health"]; !ok {
		t.Errorf("java-health not injected: %v", pf)
	}
	// `apps` is consumed, not left in the resolved tree.
	if _, ok := resolved.Tree["apps"]; ok {
		t.Errorf("apps key should be consumed during resolution")
	}
}

func TestAppsLinkAcceptsCatalogAlias(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/dbus.yml": `
kind: app
name: dbus
catalog_aliases: [dbus-daemon]
binary: /usr/bin/dbus-daemon
preflight:
  binary: { type: binary, path: "${binary}" }
`,
		"catalog/dbus.yml": `
kind: daemon
name: dbus
apps: [dbus-daemon]
preflight:
  config: { type: command, command: ["${dbus_binary}", "--check"] }
checks:
  service: { type: service, expect: active }
`,
		"enabled/dbus-main.yml": `
kind: service
name: dbus-main
uses: dbus
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("dbus-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	pf := nested(t, resolved.Tree, "preflight")
	if got := cfgval.String(nested(t, pf, "dbus-daemon-binary")["path"]); got != "/usr/bin/dbus-daemon" {
		t.Fatalf("alias-linked app binary path = %q, want /usr/bin/dbus-daemon", got)
	}
	configCmd, _ := nested(t, pf, "config")["command"].([]any)
	if got := fmt.Sprint(configCmd...); got != "/usr/bin/dbus-daemon--check" {
		t.Fatalf("alias-linked canonical app variable command = %v, want dbus binary", configCmd)
	}
	if names := cfg.DaemonsInCategory(CategoryApp); strings.Join(names, ",") != "dbus" {
		t.Fatalf("listed apps = %v, want only canonical dbus", names)
	}
}

func TestAppsExposeNamespacedVariables(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/cupsd.yml": `
kind: app
name: cupsd
binary: /usr/sbin/cupsd
variables: { cups_config: /usr/bin/cups-config }
preflight:
  binary: { type: binary, path: "${binary}" }
  cups-config: { type: binary, path: "${cups_config}" }
`,
		"catalog/cups.yml": `
kind: daemon
name: cups
apps: [cupsd]
preflight:
  config: { type: command, command: ["${cupsd_binary}", "-t"] }
  version: { type: command, command: ["${cupsd_cups_config}", "--version"] }
checks:
  service: { type: service, expect: active }
`,
		"enabled/cups.yml": `
kind: service
name: cups
uses: cups
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("cups")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	preflight := nested(t, resolved.Tree, "preflight")
	configCmd, _ := nested(t, preflight, "config")["command"].([]any)
	if got := fmt.Sprint(configCmd...); got != "/usr/sbin/cupsd-t" {
		t.Fatalf("config command = %v, want cupsd binary from app", configCmd)
	}
	versionCmd, _ := nested(t, preflight, "version")["command"].([]any)
	if got := fmt.Sprint(versionCmd...); got != "/usr/bin/cups-config--version" {
		t.Fatalf("version command = %v, want extra app variable", versionCmd)
	}
}

func TestSingleAppExposesDefaultVariables(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/php-fpm.yml": `
kind: app
name: php-fpm
binary: /usr/bin/php-fpm
variables: { config: /etc/php-fpm.conf }
preflight:
  binary: { type: binary, path: "${binary}" }
`,
		"catalog/php-fpm.yml": `
kind: daemon
name: php-fpm
apps: [php-fpm]
preflight:
  config: { type: command, command: ["${binary}", "--test", "--fpm-config", "${config}"] }
processes:
  main: { type: command_match, exe: "${binary}", user: root }
checks:
  service: { type: service, expect: active }
`,
		"enabled/php-fpm.yml": `
kind: service
name: php-fpm
uses: php-fpm
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("php-fpm")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	preflight := nested(t, resolved.Tree, "preflight")
	configCmd, _ := nested(t, preflight, "config")["command"].([]any)
	if got := fmt.Sprint(configCmd...); got != "/usr/bin/php-fpm--test--fpm-config/etc/php-fpm.conf" {
		t.Fatalf("config command = %v, want defaults from linked app", configCmd)
	}
	main := nested(t, resolved.Tree, "processes", "main")
	if got := cfgval.String(main["exe"]); got != "/usr/bin/php-fpm" {
		t.Fatalf("process exe = %q, want app binary", got)
	}
	if _, ok := preflight["php-fpm-binary"]; !ok {
		t.Fatalf("app binary preflight should still be injected with namespace: %v", preflight)
	}
}

func TestServiceVariablesOverrideAppVariables(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/cupsd.yml": `
kind: app
name: cupsd
binary: /usr/sbin/cupsd
preflight:
  binary: { type: binary, path: "${binary}" }
`,
		"catalog/cups.yml": `
kind: daemon
name: cups
apps: [cupsd]
variables: { cupsd_binary: /opt/cups/sbin/cupsd }
preflight:
  config: { type: command, command: ["${cupsd_binary}", "-t"] }
checks:
  service: { type: service, expect: active }
`,
		"enabled/cups.yml": `
kind: service
name: cups
uses: cups
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("cups")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	configCmd, _ := nested(t, nested(t, resolved.Tree, "preflight"), "config")["command"].([]any)
	if got := fmt.Sprint(configCmd...); got != "/opt/cups/sbin/cupsd-t" {
		t.Fatalf("config command = %v, want service variable override", configCmd)
	}
}

func TestServiceVariablesOverrideSingleAppDefaults(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/php-fpm.yml": `
kind: app
name: php-fpm
binary: /usr/bin/php-fpm
preflight:
  binary: { type: binary, path: "${binary}" }
`,
		"catalog/php-fpm.yml": `
kind: daemon
name: php-fpm
apps: [php-fpm]
binary: /opt/php/sbin/php-fpm
preflight:
  config: { type: command, command: ["${binary}", "--test"] }
checks:
  service: { type: service, expect: active }
`,
		"enabled/php-fpm.yml": `
kind: service
name: php-fpm
uses: php-fpm
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("php-fpm")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	configCmd, _ := nested(t, nested(t, resolved.Tree, "preflight"), "config")["command"].([]any)
	if got := fmt.Sprint(configCmd...); got != "/opt/php/sbin/php-fpm--test" {
		t.Fatalf("config command = %v, want local binary override", configCmd)
	}
	appBinary := nested(t, nested(t, resolved.Tree, "preflight"), "php-fpm-binary")
	if got := cfgval.String(appBinary["path"]); got != "/usr/bin/php-fpm" {
		t.Fatalf("app binary preflight path = %q, want app-owned binary", got)
	}
}

func TestAppsLinkUnknownAppErrors(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/web.yml": `
kind: daemon
name: web
apps: [no-such-app]
variables: { port: 80 }
checks:
  port: { type: tcp, port: "${port}" }
`,
		"enabled/web-main.yml": `
kind: service
name: web-main
uses: web
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, errs := cfg.Resolve("web-main")
	if len(errs) == 0 {
		t.Fatal("linking an unknown app must error")
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
	enabledDir := filepath.Join(configDir, "apps")
	catalogDir := filepath.Join(root, "catalog")
	for _, d := range []string{enabledDir, catalogDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "redis.yml"), []byte(`
kind: daemon
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
  catalog: [../catalog]
  includes: [apps]
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
	if got := cfg.Global.Includes[0]; got != enabledDir {
		t.Fatalf("Includes[0] = %q, want %q", got, enabledDir)
	}
	if got := cfg.Global.Catalog[0]; got != catalogDir {
		t.Fatalf("Catalog[0] = %q, want %q", got, catalogDir)
	}
	if len(cfg.Services) != 1 {
		t.Fatalf("Services = %d, want 1", len(cfg.Services))
	}
	watches, _ := cfg.Global.Raw["watches"].(map[string]any)
	if len(watches) != 1 {
		t.Fatalf("watches in global config = %d, want 1", len(watches))
	}
}

func TestDefaultIncludeDirsPreferServicesAndKeepAppsAlias(t *testing.T) {
	want := []string{"/etc/sermo/services", "/etc/sermo/apps"}
	if strings.Join(defaultIncludeDirs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("defaultIncludeDirs = %v, want %v", defaultIncludeDirs, want)
	}
}

func TestLoadUsesDefaultIncludeDirsWhenIncludesOmitted(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  catalog: [ @ROOT@/catalog ]
  runtime: /run/sermo
`,
		"services/web.yml": `
kind: service
name: web
`,
	})
	root := filepath.Dir(global)
	oldDefaultIncludeDirs := defaultIncludeDirs
	defaultIncludeDirs = []string{filepath.Join(root, "services"), filepath.Join(root, "apps")}
	t.Cleanup(func() { defaultIncludeDirs = oldDefaultIncludeDirs })

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := strings.Join(cfg.Global.Includes, "\n"); got != strings.Join(defaultIncludeDirs, "\n") {
		t.Fatalf("Global.Includes = %v, want %v", cfg.Global.Includes, defaultIncludeDirs)
	}
	if _, ok := cfg.Services["web"]; !ok {
		t.Fatalf("service from default services include was not loaded")
	}
}

func TestLoadLegacyEnabledPathAlias(t *testing.T) {
	root := t.TempDir()
	includeDir := filepath.Join(root, "enabled")
	if err := os.MkdirAll(includeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(`
paths:
  enabled: [enabled]
defaults:
  policy: { cooldown: 5m }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Global.Includes) != 1 || cfg.Global.Includes[0] != includeDir {
		t.Fatalf("Includes = %v, want [%s]", cfg.Global.Includes, includeDir)
	}
}

func TestLoadIncludedWatchFragments(t *testing.T) {
	root := t.TempDir()
	enabled := filepath.Join(root, "enabled")
	if err := os.MkdirAll(filepath.Join(enabled, "volume"), 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(`
paths:
  includes: [enabled]
defaults:
  policy: { cooldown: 5m }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(enabled, "volume", "disk-root.yml"), []byte(`
watches:
  disk-root:
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
	watches, _ := cfg.Global.Raw["watches"].(map[string]any)
	if _, ok := watches["disk-root"]; !ok {
		t.Fatalf("watch fragment not loaded: %v", watches)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("fragment config should validate, got %v", issues)
	}
}

func TestLoadIncludedWatchFragmentRejectsDuplicate(t *testing.T) {
	root := t.TempDir()
	enabled := filepath.Join(root, "enabled")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(`
paths:
  includes: [enabled]
watches:
  disk-root:
    check: { type: disk, path: /, used_pct: { op: ">=", value: 90 } }
    then:
      hook: { command: [/bin/true] }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(enabled, "disk-root.yml"), []byte(`
watches:
  disk-root:
    check: { type: disk, path: /, used_pct: { op: ">=", value: 95 } }
    then:
      hook: { command: [/bin/true] }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(global); err == nil || !strings.Contains(err.Error(), `watch "disk-root" is already defined`) {
		t.Fatalf("Load() error = %v, want duplicate watch", err)
	}
}

func TestValidateGlobalErrors(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
engine:
  backend: bogus
paths:
  catalog: [ @ROOT@/catalog ]
  includes: [ @ROOT@/enabled ]
  locks: /run/sermo/locks
  runtime: relative/path
  templates: relative/templates
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
		"paths.templates",
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
paths: { includes: [ @ROOT@/enabled ] }
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
paths: { includes: [ @ROOT@/enabled ] }
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
paths: { includes: [ @ROOT@/enabled ] }
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
			"tool": []any{missing, present},
		},
	})
	if vars["tool"] != present {
		t.Errorf("tool = %q, want first existing %q", vars["tool"], present)
	}

	// Stops at the first hit even when a later candidate also exists.
	vars = collectVariables(map[string]any{
		"variables": map[string]any{
			"tool": []any{present, missing},
		},
	})
	if vars["tool"] != present {
		t.Errorf("tool = %q, want %q", vars["tool"], present)
	}

	// None exist: falls back to the first candidate so the value stays usable.
	other := filepath.Join(dir, "also-missing")
	vars = collectVariables(map[string]any{
		"variables": map[string]any{
			"tool": []any{missing, other},
		},
	})
	if vars["tool"] != missing {
		t.Errorf("tool = %q, want fallback to first %q", vars["tool"], missing)
	}

	// A null/empty first element must not become the fallback: the value should
	// stay a well-formed (if missing) path, not an empty string.
	vars = collectVariables(map[string]any{
		"variables": map[string]any{
			"tool": []any{nil, missing},
		},
	})
	if vars["tool"] != missing {
		t.Errorf("tool = %q, want fallback to first non-empty %q", vars["tool"], missing)
	}
}

func TestTopLevelBinaryDesugarsAndPrefersExecutable(t *testing.T) {
	dir := t.TempDir()
	notExec := filepath.Join(dir, "not-exec")
	if err := os.WriteFile(notExec, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	execPath := filepath.Join(dir, "exec")
	if err := os.WriteFile(execPath, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/app.yml": `
kind: daemon
name: app
binary:
  - ` + notExec + `
  - ` + execPath + `
checks:
  process: { type: process, exe: "${binary}", user: root }
`,
		"enabled/app-main.yml": "kind: service\nname: app-main\nuses: app\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("app-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if _, present := resolved.Tree["binary"]; present {
		t.Fatalf("top-level binary must be consumed in resolved config")
	}
	vars := nested(t, resolved.Tree, "variables")
	if got := cfgval.String(vars["binary"]); got != execPath {
		t.Fatalf("variables.binary = %q, want executable %q", got, execPath)
	}
	bin := nested(t, nested(t, resolved.Tree, "preflight"), "binary")
	if got := cfgval.String(bin["path"]); got != execPath {
		t.Fatalf("preflight.binary.path = %q, want %q", got, execPath)
	}
	proc := nested(t, nested(t, resolved.Tree, "checks"), "process")
	if got := cfgval.String(proc["exe"]); got != execPath {
		t.Fatalf("process exe = %q, want %q", got, execPath)
	}
}

func TestTopLevelLibraryBinaryDoesNotGenerateExecutablePreflight(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/libs/libdemo.yml": `
kind: lib
name: libdemo
binary: /usr/lib64/libdemo.so.1
preflight:
  version: { type: command, command: ["/usr/bin/strings", "${binary}"], timeout: 10s, optional: true }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.ResolveCatalog(CategoryLibrary, "libdemo")
	if len(errs) != 0 {
		t.Fatalf("ResolveCatalog() errors = %v", errs)
	}
	vars := nested(t, resolved.Tree, "variables")
	if got := cfgval.String(vars["binary"]); got != "/usr/lib64/libdemo.so.1" {
		t.Fatalf("variables.binary = %q, want library path", got)
	}
	preflight := nested(t, resolved.Tree, "preflight")
	if _, present := preflight["binary"]; present {
		t.Fatalf("library must not generate executable binary preflight: %v", preflight)
	}
}

func TestVariableBinaryRejected(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/app.yml": `
kind: daemon
name: app
variables: { binary: /usr/local/bin/app }
`,
		"enabled/app-main.yml": "kind: service\nname: app-main\nuses: app\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	if !hasIssue(issues, "variables.binary is not supported") {
		t.Fatalf("Validate issues = %v, want variables.binary rejection", issues)
	}
}

func TestPidfileRejectsRelativeCandidate(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/app.yml": `
kind: daemon
name: app
pidfile: run/app.pid
`,
		"enabled/app-main.yml": "kind: service\nname: app-main\nuses: app\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	if !hasIssue(issues, `pidfile path "run/app.pid" must be absolute`) {
		t.Fatalf("Validate issues = %v, want relative pidfile issue", issues)
	}
}

func TestBuiltinNameAndDisplayNameVariables(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/db.yml": `
kind: daemon
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
		// Inherits the daemon's display_name; name is its own.
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
		if got := cfgval.String(then["message"]); got != want {
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

func TestCategoryLabelFallsBack(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"present", map[string]any{"category": "database"}, "database"},
		{"trimmed", map[string]any{"category": " database "}, "database"},
		{"no-inference-from-name", map[string]any{"name": "nginx"}, "service"},
		{"no-inference-from-display-name", map[string]any{"display_name": "MariaDB"}, "service"},
		{"absent", map[string]any{}, "service"},
		{"blank", map[string]any{"category": "   "}, "service"},
		{"non-string", map[string]any{"category": 7}, "service"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CategoryLabel(tc.body, "service"); got != tc.want {
				t.Errorf("CategoryLabel(%v) = %q, want %q", tc.body, got, tc.want)
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
		"catalog/bad.yml": `
kind: daemon
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
		t.Fatalf("missing daemon name issue in %v", issues)
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
	if cfgval.String(policy["max_actions"]) != "3" {
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
	if got := cfgval.String(nested(t, resolved.Tree, "checks", "ping")["host"]); got != "myhost" {
		t.Errorf("ping host = %q, want myhost", got)
	}
	// ${service} → the backend unit name; ${host} resolved; runtime vars deferred.
	msg := cfgval.String(nested(t, resolved.Tree, "rules", "alert-down", "then")["message"])
	if !strings.Contains(msg, "nginx on myhost") {
		t.Errorf("message = %q, want service/host substituted", msg)
	}
	for _, lit := range []string{"${event}", "${action}", "${date}"} {
		if !strings.Contains(msg, lit) {
			t.Errorf("message = %q, want %s left for runtime", msg, lit)
		}
	}
}

func TestBuiltinInitUserPidfileVars(t *testing.T) {
	oldInit, oldUser := detectedInit, detectedUser
	detectedInit, detectedUser = "openrc", "sermo"
	defer func() { detectedInit, detectedUser = oldInit, oldUser }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/db.yml": `
kind: service
name: db
service: { name: postgresql }
processes:
  main: { type: pidfile, path: "${pidfile}" }
checks:
  who: { type: command, command: ["id", "-u", "${user}"] }
  init: { type: command, command: ["echo", "${init}"] }
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
	if got := cfgval.String(nested(t, resolved.Tree, "processes", "main")["path"]); got != "/run/postgresql.pid" {
		t.Errorf("${pidfile} = %q, want /run/postgresql.pid", got)
	}
	who, _ := nested(t, resolved.Tree, "checks", "who")["command"].([]any)
	if len(who) != 3 || who[2] != "sermo" {
		t.Errorf("${user} = %v, want sermo", who)
	}
	in, _ := nested(t, resolved.Tree, "checks", "init")["command"].([]any)
	if len(in) != 2 || in[1] != "openrc" {
		t.Errorf("${init} = %v, want openrc", in)
	}
}

func TestUserVariableOverridesBuiltinUserPidfile(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/db.yml": `
kind: service
name: db
service: { name: postgresql }
variables:
  user: postgres
  pidfile: /run/postgresql/main.pid
processes:
  main: { type: pidfile, path: "${pidfile}" }
checks:
  who: { type: command, command: ["id", "${user}"] }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, _ := cfg.Resolve("db")
	if got := cfgval.String(nested(t, resolved.Tree, "processes", "main")["path"]); got != "/run/postgresql/main.pid" {
		t.Errorf("pidfile = %q, want the explicit variable", got)
	}
	who, _ := nested(t, resolved.Tree, "checks", "who")["command"].([]any)
	if len(who) != 2 || who[1] != "postgres" {
		t.Errorf("user = %v, want explicit postgres", who)
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
	if got := cfgval.String(nested(t, resolved.Tree, "checks", "ping")["host"]); got != "127.0.0.1" {
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
	if got := cfgval.String(nested(t, resolved.Tree, "checks", "ping")["port"]); got != "6379" {
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
	if got := cfgval.String(nested(t, resolved.Tree, "checks", "ping")["port"]); got != "7000" {
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
		"catalog/apache.yml": `
kind: daemon
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
	body := cfg.Daemons["apache"].Body

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
	if cfgval.String(http["timeout"]) != "5s" || cfgval.String(http["url"]) != "http://localhost/gentoo" {
		t.Errorf("checks.http = %v, want timeout 5s + gentoo url", http)
	}

	// policy: gentoo absent → the default branch applies.
	policy := body["policy"].(map[string]any)
	if cfgval.String(policy["cooldown"]) != "9m" {
		t.Errorf("policy.cooldown = %v, want default 9m", policy["cooldown"])
	}
}

func TestOSSelectorListBranch(t *testing.T) {
	old := detectedOS
	detectedOS = "gentoo"
	defer func() { detectedOS = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/db.yml": `
kind: daemon
name: db
processes:
  main:
    type: pidfile
    path:
      os:
        gentoo: [/run/db1.pid, /run/db.pid]
        default: [/run/db.pid]
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	main := nested(t, cfg.Daemons["db"].Body, "processes", "main")
	got, _ := main["path"].([]any)
	if len(got) != 2 || got[0] != "/run/db1.pid" {
		t.Errorf("path = %v, want the gentoo candidate list", main["path"])
	}
}

func TestOSVariableBaked(t *testing.T) {
	old := detectedOS
	detectedOS = "debian"
	defer func() { detectedOS = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/app.yml": `
kind: daemon
name: app
binary: "/opt/${os}/bin/app"
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := daemonBinary(cfg.Daemons["app"].Body); got != "/opt/debian/bin/app" {
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
		"catalog/qemu.yml": `
kind: daemon
name: qemu
display_name: "QEMU"
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
	if got := daemonBinary(cfg.Daemons["qemu"].Body); got != "/usr/bin/qemu-system-aarch64" {
		t.Errorf("baked binary = %q, want /usr/bin/qemu-system-aarch64", got)
	}
	resolved, errs := cfg.ResolveDaemon("qemu")
	if len(errs) != 0 {
		t.Fatalf("ResolveDaemon() errors = %v", errs)
	}
	bin := nested(t, resolved.Tree, "preflight", "binary")
	if cfgval.String(bin["path"]) != "/usr/bin/qemu-system-aarch64" {
		t.Errorf("resolved binary path = %v, want /usr/bin/qemu-system-aarch64", bin["path"])
	}
}

func TestDaemonCategoryFromDirectory(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml":              baseGlobal,
		"catalog/nginx.yml":      "kind: daemon\nname: nginx\nservice: { name: nginx }\n",
		"catalog/apps/git.yml":   "kind: daemon\nname: git\nservice: { name: git }\n",
		"catalog/libs/glibc.yml": "kind: daemon\nname: glibc\nbinary: /lib64/libc.so.6\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// The subdirectory determines the kind (and registry): a catalog root file is
	// a daemon, apps/ an app, libs/ a lib — even though each fixture declares
	// `kind: daemon`, the loader derives it from the directory.
	cases := []struct {
		name, wantCat string
		reg           map[string]*Document
	}{
		{"nginx", CategoryService, cfg.Daemons},
		{"git", CategoryApp, cfg.Apps},
		{"glibc", CategoryLibrary, cfg.Libraries},
	}
	for _, tc := range cases {
		doc, ok := tc.reg[tc.name]
		if !ok {
			t.Fatalf("%q not loaded in its registry", tc.name)
		}
		if doc.Category != tc.wantCat {
			t.Errorf("%s category = %q, want %q", tc.name, doc.Category, tc.wantCat)
		}
	}
	if got := cfg.DaemonsInCategory(CategoryApp); len(got) != 1 || got[0] != "git" {
		t.Errorf("DaemonsInCategory(app) = %v, want [git]", got)
	}
}

func TestCatalogAliasDoesNotShadowCanonicalDaemon(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/a.yml": `
kind: daemon
name: a
catalog_aliases: [b]
service: { name: a }
`,
		"catalog/b.yml": `
kind: daemon
name: b
service: { name: b }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	doc, ok := cfg.Daemons["b"]
	if !ok {
		t.Fatal("daemon b is not indexed")
	}
	if doc.Name != "b" {
		t.Fatalf("daemon b resolves to %q, want canonical b", doc.Name)
	}
	resolved, errs := cfg.ResolveCatalog(CategoryService, "b")
	if len(errs) > 0 {
		t.Fatalf("ResolveCatalog(b) errors = %v", errs)
	}
	if resolved.Name != "b" {
		t.Fatalf("ResolveCatalog(b) = %q, want b", resolved.Name)
	}
}

func TestReloadOnChangeDesugarsToReloadRule(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/udev.yml": `
kind: service
name: udev
service: { name: systemd-udevd }
reload_on_change:
  paths:
    - /etc/udev/rules.d
    - /lib/udev/rules.d
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("udev")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if _, present := resolved.Tree["reload_on_change"]; present {
		t.Errorf("reload_on_change should be desugared away")
	}
	for i, wantPath := range []string{"/etc/udev/rules.d", "/lib/udev/rules.d"} {
		rule := fmt.Sprintf("reload-on-change-%d", i+1)
		then := nested(t, resolved.Tree, "rules", rule, "then")
		if cfgval.String(then["action"]) != "reload" {
			t.Errorf("%s action = %v, want reload", rule, then["action"])
		}
		changed := nested(t, resolved.Tree, "rules", rule, "if", "changed")
		if cfgval.String(changed["path"]) != wantPath {
			t.Errorf("%s changed.path = %v, want %q", rule, changed["path"], wantPath)
		}
	}
}

func TestRestartOnChangeDesugarsToChangedRule(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/libs/glibc.yml": `
kind: daemon
name: glibc
display_name: "GNU C Library"
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
	if cfgval.String(then["action"]) != "restart" {
		t.Errorf("generated rule action = %v, want restart", then["action"])
	}
	changed := nested(t, resolved.Tree, "rules", "restart-on-change-glibc", "if", "changed")
	if cfgval.String(changed["path"]) != "/lib64/libc.so.6" {
		t.Errorf("changed.path = %v, want /lib64/libc.so.6", changed["path"])
	}
}

func TestRestartOnChangeUnknownLibraryErrors(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		// nginx is a service daemon, not a library: referencing it must error.
		"catalog/nginx.yml": "kind: daemon\nname: nginx\nservice: { name: nginx }\n",
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
	itok := *tokenFor("x%i")
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

	initd := filepath.Join(root, "init.d")
	if err := os.MkdirAll(initd, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"openvpn.tun1", "openvpn.client-a", "openvpn._bad", "openvpn"} {
		if err := os.WriteFile(filepath.Join(initd, f), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got = discoverVersions(initd+"/openvpn.${instance}", itok)
	want = []string{"client-a", "tun1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("instance discoverVersions = %v, want %v", got, want)
	}
}

func TestMaterializedVersionValuesUsesAllBinaryCandidates(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, v := range []string{"8.2", "8.3"} {
		dir := filepath.Join(second, "php"+v, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "php-fpm"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	tok := tokenFor("php-fpm%v")
	if tok == nil {
		t.Fatal("missing version token")
	}
	paths := []string{
		filepath.Join(first, "php${version}", "bin", "php-fpm"),
		filepath.Join(second, "php${version}", "bin", "php-fpm"),
	}
	got := materializedVersionValues(paths, nil, *tok)
	want := []string{"8.2", "8.3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("materializedVersionValues = %v, want %v", got, want)
	}
}

// TestDaemonVersionTemplateDiscoversFromLinkedApp covers a daemon template whose
// monitored binary is generic (no ${version}); installed versions come from the
// linked app template, and ${version} is baked into daemon aliases.
func TestDaemonVersionTemplateDiscoversFromLinkedApp(t *testing.T) {
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

	catalogDir := filepath.Join(root, "catalog")
	enabledDir := filepath.Join(root, "enabled")
	if err := os.MkdirAll(enabledDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(catalogDir, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	appTmpl := fmt.Sprintf(`
kind: app
name: php-fpm%%v
display_name: "PHP-FPM ${version}"
versions:
  from: "%s/php${version}/bin/php-fpm"
binary: "%s/php${version}/bin/php-fpm"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "-v"] }
`, slots, slots)
	if err := os.WriteFile(filepath.Join(catalogDir, "apps", "php-fpm%v.yml"), []byte(appTmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl := `
kind: daemon
name: php-fpm%v
display_name: "PHP-FPM ${version}"
service:
  systemd: ["php${version}-fpm"]
apps: ["php-fpm${version}"]
binary: /usr/sbin/php-fpm
`
	if err := os.WriteFile(filepath.Join(catalogDir, "php-fpm%v.yml"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], includes: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, catalogDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, v := range []string{"7.4", "8.3"} {
		doc, ok := cfg.Daemons["php-fpm"+v]
		if !ok {
			t.Fatalf("expected materialized daemon php-fpm%s", v)
		}
		// Generic binary is preserved; version did not leak into it.
		if got := daemonBinary(doc.Body); got != "/usr/sbin/php-fpm" {
			t.Errorf("php-fpm%s binary = %q, want /usr/sbin/php-fpm", v, got)
		}
		// ${version} baked into the service unit candidate.
		sysd := nested(t, doc.Body, "service")["systemd"].([]any)
		if got := sysd[0].(string); got != "php"+v+"-fpm" {
			t.Errorf("php-fpm%s service unit = %q, want php%s-fpm", v, got, v)
		}
		// Discovery metadata stripped from the concrete daemon.
		if _, present := doc.Body["versions"]; present {
			t.Errorf("php-fpm%s still carries versions block", v)
		}
	}
}

func TestDaemonVersionTemplateRequiresLinkedAppDiscovery(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "worker1.0"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/worker%v.yml": fmt.Sprintf(`
kind: daemon
name: worker%%v
binary: "%s/worker${version}"
checks: { service: { type: service, expect: active } }
`, bin),
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.Daemons["worker1.0"]; ok {
		t.Fatalf("daemon template materialized from service binary; expected linked app discovery only")
	}
}

func TestTomcatVersionTemplateLinksMaterializedApp(t *testing.T) {
	root := t.TempDir()
	tomcatRoot := filepath.Join(root, "usr", "share")
	for _, v := range []string{"9", "10"} {
		dir := filepath.Join(tomcatRoot, "tomcat-"+v, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "catalina.sh"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	catalogDir := filepath.Join(root, "catalog")
	enabledDir := filepath.Join(root, "enabled")
	write := func(dir, file, content string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	catalina := filepath.Join(tomcatRoot, "tomcat-${version}", "bin", "catalina.sh")
	write(filepath.Join(catalogDir, "apps"), "java.yml", `
kind: app
name: java
binary: /usr/bin/java
preflight:
  binary: { type: binary, path: "${binary}" }
`)
	write(filepath.Join(catalogDir, "apps"), "tomcat-%v.yml", fmt.Sprintf(`
kind: app
name: tomcat-%%v
display_name: "Apache Tomcat ${version}"
binary: %q
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "version"], timeout: 10s }
`, catalina))
	write(filepath.Join(catalogDir, "services"), "tomcat-%v.yml", `
kind: daemon
name: tomcat-%v
display_name: "Apache Tomcat ${version}"
service: tomcat
apps: [java, "tomcat-${version}"]
variables: { port: 8080 }
checks: { service: { type: service, expect: active } }
`)
	write(enabledDir, "site.yml", "kind: service\nname: site\nuses: tomcat-10\n")

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths:
  catalog: [ %s ]
  includes: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, catalogDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.Apps["tomcat-%v"]; ok {
		t.Fatalf("app template tomcat-%%v should not be registered")
	}
	for _, v := range []string{"9", "10"} {
		if _, ok := cfg.Apps["tomcat-"+v]; !ok {
			t.Fatalf("expected materialized app tomcat-%s", v)
		}
		if _, ok := cfg.Daemons["tomcat-"+v]; !ok {
			t.Fatalf("expected materialized daemon tomcat-%s", v)
		}
	}

	resolved, errs := cfg.Resolve("site")
	if len(errs) != 0 {
		t.Fatalf("Resolve(site) errors = %v", errs)
	}
	preflight := nested(t, resolved.Tree, "preflight")
	if got := cfgval.String(nested(t, preflight, "java-binary")["path"]); got != "/usr/bin/java" {
		t.Fatalf("java-binary path = %q, want /usr/bin/java", got)
	}
	wantCatalina := filepath.Join(tomcatRoot, "tomcat-10", "bin", "catalina.sh")
	if got := cfgval.String(nested(t, preflight, "tomcat-10-binary")["path"]); got != wantCatalina {
		t.Fatalf("tomcat-10-binary path = %q, want %q", got, wantCatalina)
	}
	version := nested(t, preflight, "tomcat-10-version")
	command, _ := version["command"].([]any)
	if len(command) != 2 || command[0] != wantCatalina || command[1] != "version" {
		t.Fatalf("tomcat-10-version command = %v, want [%s version]", command, wantCatalina)
	}
}

func TestVersionTemplateServiceLinksMaterializedApp(t *testing.T) {
	root := t.TempDir()
	pgRoot := filepath.Join(root, "usr", "lib64")
	for _, v := range []string{"15", "16"} {
		dir := filepath.Join(pgRoot, "postgresql-"+v, "bin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "postgres"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	catalogDir := filepath.Join(root, "catalog")
	enabledDir := filepath.Join(root, "enabled")
	write := func(dir, file, content string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(pgRoot, "postgresql-${version}", "bin", "postgres")
	write(filepath.Join(catalogDir, "apps"), "postgres-%v.yml", fmt.Sprintf(`
kind: app
name: postgres-%%v
display_name: "PostgreSQL ${version}"
binary: %q
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "--version"], timeout: 10s }
`, binary))
	write(filepath.Join(catalogDir, "services"), "postgres-%v.yml", `
kind: daemon
name: postgres-%v
display_name: "PostgreSQL ${version}"
service: "postgresql-${version}"
apps: ["postgres-${version}"]
variables: { port: 5432 }
checks: { service: { type: service, expect: active } }
`)
	write(enabledDir, "pg.yml", "kind: service\nname: pg\nuses: postgres-16\n")

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths:
  catalog: [ %s ]
  includes: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, catalogDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.Apps["postgres-%v"]; ok {
		t.Fatalf("app template postgres-%%v should not be registered")
	}
	for _, v := range []string{"15", "16"} {
		if _, ok := cfg.Apps["postgres-"+v]; !ok {
			t.Fatalf("expected materialized app postgres-%s", v)
		}
		if _, ok := cfg.Daemons["postgres-"+v]; !ok {
			t.Fatalf("expected materialized daemon postgres-%s", v)
		}
	}

	resolved, errs := cfg.Resolve("pg")
	if len(errs) != 0 {
		t.Fatalf("Resolve(pg) errors = %v", errs)
	}
	if _, ok := resolved.Tree["apps"]; ok {
		t.Fatalf("apps should be consumed during resolution: %v", resolved.Tree["apps"])
	}
	preflight := nested(t, resolved.Tree, "preflight")
	binaryCheck := nested(t, preflight, "postgres-16-binary")
	wantBinary := filepath.Join(pgRoot, "postgresql-16", "bin", "postgres")
	if got := cfgval.String(binaryCheck["path"]); got != wantBinary {
		t.Fatalf("postgres-16-binary path = %q, want %q", got, wantBinary)
	}
	versionCheck := nested(t, preflight, "postgres-16-version")
	command, _ := versionCheck["command"].([]any)
	if len(command) != 2 || command[0] != wantBinary || command[1] != "--version" {
		t.Fatalf("postgres-16-version command = %v, want [%s --version]", command, wantBinary)
	}
}

func TestVersionTemplateDiscoversFromLinkedAppTemplate(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"php-fpm8.2", "php-fpm8.4"} {
		if err := os.WriteFile(filepath.Join(bin, f), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	catalogDir := filepath.Join(root, "catalog")
	appsDir := filepath.Join(catalogDir, "apps")
	enabledDir := filepath.Join(root, "enabled")
	for _, dir := range []string{appsDir, enabledDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(appsDir, "php-fpm%v.yml"), []byte(fmt.Sprintf(`
kind: app
name: php-fpm%%v
display_name: "PHP-FPM ${version}"
binary: "%s/php-fpm${version}"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "-v"] }
`, bin)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "php-fpm%v.yml"), []byte(`
kind: daemon
name: php-fpm%v
display_name: "PHP-FPM ${version}"
apps: ["php-fpm${version}"]
preflight:
  config: { type: command, command: ["${binary}", "--test"] }
processes:
  main: { type: command_match, exe: "${binary}", user: root }
checks:
  service: { type: service, expect: active }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], includes: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, catalogDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, version := range []string{"8.2", "8.4"} {
		name := "php-fpm" + version
		if _, ok := cfg.Daemons[name]; !ok {
			t.Fatalf("expected materialized daemon %s", name)
		}
		if _, ok := cfg.Apps[name]; !ok {
			t.Fatalf("expected materialized app %s", name)
		}
		resolved, errs := cfg.ResolveCatalog(CategoryService, name)
		if len(errs) != 0 {
			t.Fatalf("ResolveCatalog(%s) errors = %v", name, errs)
		}
		wantBinary := filepath.Join(bin, name)
		configCmd, _ := nested(t, nested(t, resolved.Tree, "preflight"), "config")["command"].([]any)
		if got := fmt.Sprint(configCmd...); got != wantBinary+"--test" {
			t.Fatalf("%s config command = %v, want linked app binary", name, configCmd)
		}
		main := nested(t, resolved.Tree, "processes", "main")
		if got := cfgval.String(main["exe"]); got != wantBinary {
			t.Fatalf("%s process exe = %q, want %q", name, got, wantBinary)
		}
	}
}

func TestVersionTemplateUnversionedMaterialization(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"python", "python3", "php", "php8.4"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	catalogDir := filepath.Join(root, "catalog")
	appsDir := filepath.Join(catalogDir, "apps")
	enabledDir := filepath.Join(root, "enabled")
	for _, dir := range []string{appsDir, enabledDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(appsDir, "python%n.yml"), []byte(fmt.Sprintf(`
kind: app
name: python%%n
display_name: "Python ${n}"
description: "Python runtime ${n}"
binary: "%s/python${n}"
preflight:
  binary: { type: binary, path: "${binary}" }
`, bin)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appsDir, "php%v.yml"), []byte(fmt.Sprintf(`
kind: app
name: php%%v
display_name: "PHP ${version}"
description: "PHP runtime ${version}"
binary: "%s/php${version}"
preflight:
  binary: { type: binary, path: "${binary}" }
`, bin)), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], includes: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, catalogDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := strings.Join(cfg.DaemonsInCategory(CategoryApp), ","); got != "php,php8.4,python,python3" {
		t.Fatalf("app names = %s, want php,php8.4,python,python3", got)
	}
	tests := []struct {
		name        string
		binary      string
		displayName string
		description string
	}{
		{"python", filepath.Join(bin, "python"), "Python", "Python runtime"},
		{"python3", filepath.Join(bin, "python3"), "Python 3", "Python runtime 3"},
		{"php", filepath.Join(bin, "php"), "PHP", "PHP runtime"},
		{"php8.4", filepath.Join(bin, "php8.4"), "PHP 8.4", "PHP runtime 8.4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, ok := cfg.Apps[tt.name]
			if !ok {
				t.Fatalf("app %q was not materialized", tt.name)
			}
			if _, present := doc.Body["versions"]; present {
				t.Fatalf("%s still carries versions block", tt.name)
			}
			if got := daemonBinary(doc.Body); got != tt.binary {
				t.Fatalf("%s binary = %q, want %q", tt.name, got, tt.binary)
			}
			if got := DisplayName(doc.Body, tt.name); got != tt.displayName {
				t.Fatalf("%s display_name = %q, want %q", tt.name, got, tt.displayName)
			}
			if got := cfgval.String(doc.Body["description"]); got != tt.description {
				t.Fatalf("%s description = %q, want %q", tt.name, got, tt.description)
			}
			resolved, errs := cfg.ResolveCatalog(CategoryApp, tt.name)
			if len(errs) > 0 {
				t.Fatalf("ResolveCatalog(%s): %v", tt.name, errs)
			}
			preflight := nested(t, resolved.Tree, "preflight", "binary")
			if got := cfgval.String(preflight["path"]); got != tt.binary {
				t.Fatalf("%s resolved binary path = %q, want %q", tt.name, got, tt.binary)
			}
		})
	}
}

func TestVersionTemplateUnversionedRequiresBinary(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "python3"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/python%n.yml": fmt.Sprintf(`
kind: app
name: python%%n
display_name: "Python ${n}"
binary: "%s/python${n}"
`, bin),
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.Apps["python"]; ok {
		t.Fatalf("python should not materialize without %s", filepath.Join(bin, "python"))
	}
	if _, ok := cfg.Apps["python3"]; !ok {
		t.Fatalf("python3 should materialize")
	}
}

func TestVersionTemplateUnversionedCanBeDisabled(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"php", "php8.4"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := Load(writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/php%v.yml": fmt.Sprintf(`
kind: app
name: php%%v
display_name: "PHP ${version}"
versions:
  unversioned: false
binary: "%s/php${version}"
`, bin),
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.Apps["php"]; ok {
		t.Fatalf("php should not materialize when versions.unversioned is false")
	}
	if _, ok := cfg.Apps["php8.4"]; !ok {
		t.Fatalf("php8.4 should materialize")
	}
}

func TestVersionTemplateUnversionedCanOverrideMetadata(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "php"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/php%v.yml": fmt.Sprintf(`
kind: app
name: php%%v
display_name: "PHP ${version}"
description: "PHP runtime ${version}"
versions:
  unversioned:
    display_name: "System PHP"
    description: "Default PHP interpreter"
binary: "%s/php${version}"
`, bin),
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	doc, ok := cfg.Apps["php"]
	if !ok {
		t.Fatalf("php should materialize")
	}
	if got := DisplayName(doc.Body, "php"); got != "System PHP" {
		t.Fatalf("php display_name = %q, want System PHP", got)
	}
	if got := cfgval.String(doc.Body["description"]); got != "Default PHP interpreter" {
		t.Fatalf("php description = %q, want Default PHP interpreter", got)
	}
}

func TestVersionTemplateSkipsExistingCanonicalName(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "python3"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/python%n.yml": fmt.Sprintf(`
kind: app
name: python%%n
display_name: "Python ${n}"
binary: "%s/python${n}"
`, bin),
		"catalog/apps/python3.yml": fmt.Sprintf(`
kind: app
name: python3
display_name: "Python Three"
binary: "%s/python3"
`, bin),
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := strings.Join(cfg.DaemonsInCategory(CategoryApp), ","); got != "python3" {
		t.Fatalf("app names = %s, want python3", got)
	}
	if got := DisplayName(cfg.Apps["python3"].Body, "python3"); got != "Python Three" {
		t.Fatalf("python3 display_name = %q, want explicit canonical app", got)
	}
}

func TestInstanceTemplateMaterialization(t *testing.T) {
	root := t.TempDir()
	initd := filepath.Join(root, "init.d")
	if err := os.MkdirAll(initd, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"openvpn.tun1", "openvpn.client-a"} {
		if err := os.WriteFile(filepath.Join(initd, f), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	catalogDir := filepath.Join(root, "catalog")
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
	write(catalogDir, "openvpn.yml", `
kind: daemon
name: openvpn
display_name: OpenVPN
service: openvpn
variables: { port: 1194 }
checks:
  port: { type: openvpn, port: "${port}" }
`)
	write(filepath.Join(catalogDir, "apps"), "openvpn-%i.yml", fmt.Sprintf(`
kind: app
name: openvpn-%%i
display_name: "OpenVPN ${instance}"
versions:
  from: "%s/openvpn.${instance}"
binary: /usr/bin/openvpn
preflight:
  binary: { type: binary, path: "${binary}" }
`, initd))
	tmpl := `
kind: daemon
name: openvpn-%i
uses: openvpn
display_name: "OpenVPN ${instance}"
service: "openvpn.${instance}"
apps: ["openvpn-${instance}"]
variables:
  config: "/etc/openvpn/${instance}.conf"
`
	write(catalogDir, "openvpn-%i.yml", tmpl)
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], includes: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, catalogDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.Daemons["openvpn-%i"]; ok {
		t.Fatal("template openvpn-%i should not be registered")
	}
	for _, inst := range []string{"client-a", "tun1"} {
		name := "openvpn-" + inst
		doc, ok := cfg.Daemons[name]
		if !ok {
			t.Fatalf("expected materialized daemon %q", name)
		}
		if got := ServiceUnit(doc.Body, name); got != "openvpn."+inst {
			t.Fatalf("%s service unit = %q, want openvpn.%s", name, got, inst)
		}
		if got := DisplayName(doc.Body, name); got != "OpenVPN "+inst {
			t.Fatalf("%s display_name = %q, want OpenVPN %s", name, got, inst)
		}
		vars := nested(t, doc.Body, "variables")
		if got := cfgval.String(vars["config"]); got != "/etc/openvpn/"+inst+".conf" {
			t.Fatalf("%s config = %q, want /etc/openvpn/%s.conf", name, got, inst)
		}
		if _, ok := nested(t, doc.Body, "checks")["port"]; !ok {
			t.Fatalf("%s did not inherit base checks", name)
		}
	}
}

// TestVersionTemplateMaterialization exercises a `name: foo-%v` daemon template:
// it must produce one daemon per installed app version, inherit a `uses` base,
// and drop the template itself.
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

	daemonsDir := filepath.Join(root, "daemons")
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
	write(daemonsDir, "php-fpm.yml", `
kind: daemon
name: php-fpm
display_name: "PHP-FPM"
service: { name: php-fpm }
binary: /usr/sbin/php-fpm
variables:
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
	write(filepath.Join(daemonsDir, "apps"), "php-fpm-%v.yml", fmt.Sprintf(`
kind: app
name: php-fpm-%%v
display_name: "PHP-FPM ${version}"
binary: "%s/php${version}/bin/php-fpm"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "-v"] }
`, binRoot))
	// Version template inheriting the base; installed versions come from the app.
	write(daemonsDir, "php-fpm-%v.yml", `
kind: daemon
name: php-fpm-%v
uses: php-fpm
display_name: "PHP-FPM ${version}"
apps: ["php-fpm-${version}"]
`)

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths:
  catalog: [ %s ]
  includes: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, daemonsDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Template must be gone; one concrete daemon per installed version present.
	if _, ok := cfg.Daemons["php-fpm-%v"]; ok {
		t.Errorf("template php-fpm-%%v should not be registered")
	}
	for _, v := range []string{"7.4", "8.3"} {
		name := "php-fpm-" + v
		doc, ok := cfg.Daemons[name]
		if !ok {
			t.Fatalf("expected materialized daemon %q", name)
		}
		// display_name has the version baked in (no literal ${version}).
		if got := DisplayName(doc.Body, name); got != "PHP-FPM "+v {
			t.Errorf("%s display_name = %q, want %q", name, got, "PHP-FPM "+v)
		}
		// Inherited the base rule; the versioned binary belongs to the linked app.
		wantBin := fmt.Sprintf("%s/php%s/bin/php-fpm", binRoot, v)
		if got := daemonBinary(doc.Body); got != "/usr/sbin/php-fpm" {
			t.Errorf("%s binary = %q, want inherited /usr/sbin/php-fpm", name, got)
		}
		appDoc, ok := cfg.Apps[name]
		if !ok {
			t.Fatalf("expected materialized app %q", name)
		}
		if got := daemonBinary(appDoc.Body); got != wantBin {
			t.Errorf("%s app binary = %q, want %q", name, got, wantBin)
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
	if got := cfgval.String(then["message"]); got != "PHP-FPM 8.3 configuration is invalid" {
		t.Errorf("message = %q, want %q", got, "PHP-FPM 8.3 configuration is invalid")
	}
	binaryCheck := nested(t, resolved.Tree, "preflight", "php-fpm-8.3-binary")
	wantBinary := fmt.Sprintf("%s/php8.3/bin/php-fpm", binRoot)
	if got := cfgval.String(binaryCheck["path"]); got != wantBinary {
		t.Errorf("linked app binary path = %q, want %q", got, wantBinary)
	}
}

func TestDetectHostname(t *testing.T) {
	// SERMO_HOSTNAME is taken verbatim (like SERMO_HOST), even an FQDN.
	t.Setenv("SERMO_HOSTNAME", "forced.example.com")
	if got := detectHostname(); got != "forced.example.com" {
		t.Fatalf("SERMO_HOSTNAME should be verbatim, got %q", got)
	}
	// Without the override, os.Hostname() is reduced to its short form, so the
	// result never carries a domain dot.
	t.Setenv("SERMO_HOSTNAME", "")
	if got := detectHostname(); strings.Contains(got, ".") {
		t.Fatalf("hostname should be short (no dot), got %q", got)
	}
}

func TestBuiltinHostnameVar(t *testing.T) {
	old := detectedHostname
	detectedHostname = "radon"
	defer func() { detectedHostname = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/mon.yml": `
kind: service
name: mon
service: "ceph-mon@${hostname}"
checks:
  svc: { type: service, expect: active }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("mon")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	// ${hostname} fills the instance id from the short hostname.
	if got := ServiceUnit(resolved.Tree, "mon"); got != "ceph-mon@radon" {
		t.Errorf("service unit = %q, want ceph-mon@radon", got)
	}
}

func TestUserHostnameVariableOverridesBuiltin(t *testing.T) {
	old := detectedHostname
	detectedHostname = "radon"
	defer func() { detectedHostname = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/mon.yml": `
kind: service
name: mon
service: "ceph-mon@${hostname}"
variables:
  hostname: custom-id
checks:
  svc: { type: service, expect: active }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, _ := cfg.Resolve("mon")
	if got := ServiceUnit(resolved.Tree, "mon"); got != "ceph-mon@custom-id" {
		t.Errorf("service unit = %q, want user-defined ceph-mon@custom-id", got)
	}
}

func TestVersionTemplateCephOSD(t *testing.T) {
	root := t.TempDir()
	osdRoot := filepath.Join(root, "var", "lib", "ceph", "osd")
	// Discoverable OSDs 0, 1, 3 (2 absent, to prove discovery, not a fixed range).
	for _, id := range []string{"0", "1", "3"} {
		if err := os.MkdirAll(filepath.Join(osdRoot, "ceph-"+id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Catalog files take their kind from the subdirectory (services/ → daemon,
	// apps/ → app), so the template and its app must live in the right dirs.
	catalogDir := filepath.Join(root, "catalog")
	enabledDir := filepath.Join(root, "enabled")
	write := func(dir, file, content string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(catalogDir, "apps"), "ceph-osd%n.yml", fmt.Sprintf(`
kind: app
name: ceph-osd%%n
display_name: "Ceph OSD ${n}"
versions: { from: "%s/ceph-${n}" }
binary: /usr/bin/ceph-osd
preflight:
  binary: { type: binary, path: "${binary}" }
`, osdRoot))
	write(filepath.Join(catalogDir, "services"), "ceph-osd-%n.yml", `
kind: daemon
name: ceph-osd%n
display_name: "Ceph OSD ${n}"
service: "ceph-osd@${n}"
apps: ["ceph-osd${n}"]
variables: { user: ceph }
checks: { service: { type: service, expect: active } }
`)
	// One enabled service per OSD that uses the materialized daemon.
	write(enabledDir, "osd0.yml", "kind: service\nname: osd0\nuses: ceph-osd0\n")

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths:
  catalog: [ %s ]
  includes: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, catalogDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// Template gone; one concrete daemon per discovered OSD; absent id 2 not present.
	if _, ok := cfg.Daemons["ceph-osd%n"]; ok {
		t.Errorf("template ceph-osd%%n should not be registered")
	}
	if _, ok := cfg.Daemons["ceph-osd2"]; ok {
		t.Errorf("ceph-osd2 must not exist (no /var/lib/ceph/osd/ceph-2)")
	}
	for _, id := range []string{"0", "1", "3"} {
		name := "ceph-osd" + id
		doc, ok := cfg.Daemons[name]
		if !ok {
			t.Fatalf("expected materialized daemon %q", name)
		}
		// ${n} baked into the unit name at materialization.
		if got := ServiceUnit(doc.Body, name); got != "ceph-osd@"+id {
			t.Errorf("%s service unit = %q, want ceph-osd@%s", name, got, id)
		}
		if _, ok := cfg.Apps[name]; !ok {
			t.Fatalf("expected materialized app %q", name)
		}
	}
	// The app link survives materialization: a service using ceph-osd0 resolves
	// cleanly (the ceph-osd app's preflight binary check is wired in).
	resolved, errs := cfg.Resolve("osd0")
	if len(errs) != 0 {
		t.Fatalf("Resolve(osd0) errors = %v", errs)
	}
	if _, ok := resolved.Tree["preflight"].(map[string]any); !ok {
		t.Errorf("osd0 missing preflight from linked ceph-osd app: %v", resolved.Tree)
	}
}

func TestVersionTemplateCephOSDNoMatch(t *testing.T) {
	root := t.TempDir()
	emptyRoot := filepath.Join(root, "var", "lib", "ceph", "osd") // exists, no OSDs
	if err := os.MkdirAll(emptyRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	daemonsDir := filepath.Join(root, "daemons")
	enabledDir := filepath.Join(root, "enabled")
	for _, d := range []string{daemonsDir, enabledDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	appsDir := filepath.Join(daemonsDir, "apps")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appsDir, "ceph-osd%n.yml"), []byte(fmt.Sprintf(`
kind: app
name: ceph-osd%%n
versions: { from: "%s/ceph-${n}" }
binary: /usr/bin/ceph-osd
preflight:
  binary: { type: binary, path: "${binary}" }
`, emptyRoot)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(daemonsDir, "ceph-osd-%n.yml"), []byte(`
kind: daemon
name: ceph-osd%n
service: "ceph-osd@${n}"
apps: ["ceph-osd${n}"]
variables: { user: ceph }
checks: { service: { type: service, expect: active } }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths:
  catalog: [ %s ]
  includes: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, daemonsDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() with no OSDs must not error, got %v", err)
	}
	for name := range cfg.Daemons {
		if strings.HasPrefix(name, "ceph-osd") {
			t.Errorf("no ceph-osd daemons expected with zero discovery matches, got %q", name)
		}
	}
}

func TestExpandAnalyzeResolvesUseSilenceRules(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/patterns/common.yml": `
kind: patterns
name: common
rules:
  - { id: dep,  match: "(?i)deprecated", severity: warning }
  - { id: note, match: "(?i)note",       severity: warning }
`,
		"catalog/svc.yml": `
kind: daemon
name: svc
binary: /bin/true
checks:
  config:
    type: command
    command: ["${binary}"]
    analyze:
      use: [common]
      silence: [dep]
      rules:
        - { id: local, match: "(?i)ok", severity: ok }
`,
		"enabled/svc-main.yml": "kind: service\nname: svc-main\nuses: svc\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("svc-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	checks := resolved.Tree["checks"].(map[string]any)
	analyze := checks["config"].(map[string]any)["analyze"].(map[string]any)
	rules := analyze["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("want 2 resolved rules (note + local), got %d: %v", len(rules), rules)
	}
	ids := []string{idOf(rules[0]), idOf(rules[1])}
	if ids[0] != "local" || ids[1] != "note" {
		t.Fatalf("resolved rule order = %v, want [local note] (local first for precedence, dep silenced)", ids)
	}
	if _, present := analyze["use"]; present {
		t.Errorf("use must be consumed during resolution")
	}
	if _, present := analyze["silence"]; present {
		t.Errorf("silence must be consumed during resolution")
	}
}

func idOf(r any) string { return r.(map[string]any)["id"].(string) }

func TestExpandAnalyzeUnknownSetAndBadSilence(t *testing.T) {
	mk := func(analyze string) []string {
		global := writeConfig(t, map[string]string{
			"sermo.yml":                   baseGlobal,
			"catalog/patterns/common.yml": "kind: patterns\nname: common\nrules:\n  - { id: dep, match: x, severity: warning }\n",
			"catalog/svc.yml":             "kind: daemon\nname: svc\nbinary: /bin/true\nchecks:\n  config:\n    type: command\n    command: [\"${binary}\"]\n    analyze:\n" + analyze,
			"enabled/svc-main.yml":        "kind: service\nname: svc-main\nuses: svc\n",
		})
		cfg, err := Load(global)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		_, errs := cfg.Resolve("svc-main")
		return errs
	}
	if errs := mk("      use: [nope]\n"); !hasSub(errs, "not a patterns set") {
		t.Errorf("unknown set should error, got %v", errs)
	}
	if errs := mk("      use: [common]\n      silence: [ghost]\n"); !hasSub(errs, "not present in the inherited sets") {
		t.Errorf("bad silence id should error, got %v", errs)
	}
}

func hasSub(errs []string, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

func TestExpandPidfileDesugars(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/svc.yml": `
kind: daemon
name: svc
pidfile: /run/svc.pid
checks:
  service: { type: service, expect: active }
`,
		"enabled/svc-main.yml": "kind: service\nname: svc-main\nuses: svc\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("svc-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if _, present := resolved.Tree["pidfile"]; present {
		t.Errorf("top-level pidfile key must be consumed")
	}
	// (a) process selector for tree discovery.
	procs := resolved.Tree["processes"].(map[string]any)
	sel := procs["pidfile"].(map[string]any)
	if sel["type"] != "pidfile" || sel["path"] != "/run/svc.pid" {
		t.Fatalf("process selector = %v", sel)
	}
	// (b) gated health check.
	checks := resolved.Tree["checks"].(map[string]any)
	chk := checks["pidfile"].(map[string]any)
	if chk["type"] != "pidfile" || chk["path"] != "/run/svc.pid" {
		t.Fatalf("pidfile check = %v", chk)
	}
	req, _ := chk["requires"].([]any)
	if len(req) != 1 || req[0] != "service" {
		t.Fatalf("pidfile check requires = %v, want [service]", chk["requires"])
	}
}

func TestExpandPidfileCandidateListDesugars(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/svc.yml": `
kind: daemon
name: svc
pidfile:
  - /run/svc-main.pid
  - /run/svc-legacy.pid
checks:
  service: { type: service, expect: active }
`,
		"enabled/svc-main.yml": "kind: service\nname: svc-main\nuses: svc\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("svc-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	want := []string{"/run/svc-main.pid", "/run/svc-legacy.pid"}
	procs := resolved.Tree["processes"].(map[string]any)
	sel := procs["pidfile"].(map[string]any)
	if got := cfgval.StringList(sel["path"]); !slices.Equal(got, want) {
		t.Fatalf("process pidfile paths = %v, want %v", got, want)
	}
	checks := resolved.Tree["checks"].(map[string]any)
	chk := checks["pidfile"].(map[string]any)
	if got := cfgval.StringList(chk["path"]); !slices.Equal(got, want) {
		t.Fatalf("check pidfile paths = %v, want %v", got, want)
	}
}

func TestAdditionalUnitsAndValidation(t *testing.T) {
	tree := map[string]any{
		"service":      map[string]any{"systemd": []any{"docker"}, "openrc": []any{"docker"}},
		"also_service": map[string]any{"systemd": []any{"docker.socket"}},
	}
	if got := AdditionalUnits(tree, "systemd"); len(got) != 1 || got[0] != "docker.socket" {
		t.Fatalf("AdditionalUnits systemd = %v, want [docker.socket]", got)
	}
	if got := AdditionalUnits(tree, "openrc"); len(got) != 0 {
		t.Fatalf("AdditionalUnits openrc = %v, want empty", got)
	}
}

func TestValidateAlsoServiceErrors(t *testing.T) {
	mustHave(t, validateService(t, `
kind: service
name: s
service: { systemd: [docker] }
also_service: { systemd: [docker] }
`), "primary service unit")

	mustHave(t, validateService(t, `
kind: service
name: s
service: { systemd: [docker] }
also_service: { foo: [x] }
`), "not one of systemd, openrc")
}

func TestStopInvariants(t *testing.T) {
	tree := map[string]any{
		"processes": map[string]any{
			"pidfile": map[string]any{"type": "pidfile", "path": "/run/svc.pid"},
		},
		"stop_policy": map[string]any{
			"pidfile_absent":   true,
			"files_absent":     []any{"/run/svc/*.sock"},
			"clean_after_stop": true,
		},
	}
	pp, ff, cleanEnabled, _ := StopInvariants(tree)
	if len(pp) != 1 || pp[0] != "/run/svc.pid" {
		t.Fatalf("pidfile paths = %v, want [/run/svc.pid]", pp)
	}
	if len(ff) != 1 || ff[0] != "/run/svc/*.sock" || !cleanEnabled {
		t.Fatalf("files=%v cleanEnabled=%v", ff, cleanEnabled)
	}
	// pidfile_absent omitted -> no pidfile paths even if a selector exists.
	pp2, _, _, _ := StopInvariants(map[string]any{
		"processes":   tree["processes"],
		"stop_policy": map[string]any{"files_absent": []any{"/x"}},
	})
	if len(pp2) != 0 {
		t.Fatalf("pidfile_absent off must yield no pidfile paths, got %v", pp2)
	}
}

func TestGlobalCustomVariables(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
engine: { backend: auto }
paths:
  catalog: [ @ROOT@/catalog ]
  includes: [ @ROOT@/enabled ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
  variables:
    cvar: /opt/data
    host: 10.0.0.9
`,
		"catalog/svc.yml": `
kind: daemon
name: svc
checks:
  f: { type: file_exists, path: "${cvar}/file" }
  h: { type: command, command: ["echo", "${host}"] }
`,
		"enabled/a.yml": "kind: service\nname: a\nuses: svc\n",
		// b overrides the custom host with its own variable.
		"enabled/b.yml": "kind: service\nname: b\nuses: svc\nvariables: { host: 127.0.0.1 }\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// custom var used in a service-level variable, then in a check.
	ra, errs := cfg.Resolve("a")
	if len(errs) != 0 {
		t.Fatalf("resolve a: %v", errs)
	}
	checks := ra.Tree["checks"].(map[string]any)
	if got := checks["f"].(map[string]any)["path"]; got != "/opt/data/file" {
		t.Fatalf("custom var not expanded: %v", got)
	}
	// custom host overrides the builtin host (custom > builtins).
	if got := checks["h"].(map[string]any)["command"].([]any)[1]; got != "10.0.0.9" {
		t.Fatalf("custom host should override builtin, got %v", got)
	}
	// a service's own variable overrides the custom one (service > custom).
	rb, _ := cfg.Resolve("b")
	cmd := rb.Tree["checks"].(map[string]any)["h"].(map[string]any)["command"].([]any)
	if cmd[1] != "127.0.0.1" {
		t.Fatalf("service variable should override custom, got %v", cmd[1])
	}
}

func TestGlobalBinaryVariableRejected(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
engine: { backend: auto }
paths:
  catalog: [ @ROOT@/catalog ]
  includes: [ @ROOT@/enabled ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
  variables:
    binary: /usr/bin/app
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	issues := Validate(cfg)
	if !hasIssue(issues, `defaults.variables: "binary" is reserved for top-level binary declarations`) {
		t.Fatalf("Validate issues = %v, want defaults.variables.binary rejection", issues)
	}
}

func TestResolveWatchesExpandsCustomVars(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
engine: { backend: auto }
paths:
  catalog: [ @ROOT@/catalog ]
  includes: [ @ROOT@/enabled ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
  variables: { cdir: /var/spool }
watches:
  w: { check: { type: file_exists, path: "${cdir}/flag" } }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches: %v", errs)
	}
	got := watches["w"].(map[string]any)["check"].(map[string]any)["path"]
	if got != "/var/spool/flag" {
		t.Fatalf("watch custom var not expanded: %v", got)
	}
}

func TestChangedLibraryConditionResolvesPath(t *testing.T) {
	// The documented shorthand `changed: {library: X}` resolves the library to
	// its watched file anywhere in a rule's condition tree, exactly like the
	// restart_on_change desugar.
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/libs/glibc.yml": `
kind: daemon
name: glibc
binary: "/lib64/libc.so.6"
`,
		"enabled/web.yml": `
kind: service
name: web
service: { name: web }
rules:
  glibc-changed:
    type: alert
    if:
      or:
        - changed: { library: glibc }
        - changed: { path: /etc/web.conf }
    then: { action: alert, message: "glibc changed" }
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
	or, ok := nested(t, resolved.Tree, "rules", "glibc-changed", "if")["or"].([]any)
	if !ok || len(or) != 2 {
		t.Fatalf("if.or = %v", or)
	}
	changed := nested(t, or[0].(map[string]any), "changed")
	if cfgval.String(changed["path"]) != "/lib64/libc.so.6" {
		t.Errorf("changed.path = %v, want /lib64/libc.so.6", changed["path"])
	}
}

func TestChangedUnknownLibraryErrors(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"enabled/web.yml": `
kind: service
name: web
service: { name: web }
rules:
  ghost-changed:
    type: alert
    if: { changed: { library: ghost } }
    then: { action: alert, message: "x" }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, errs := cfg.Resolve("web")
	joined := strings.Join(errs, "\n")
	if !strings.Contains(joined, `"ghost"`) || !strings.Contains(joined, "not a library") {
		t.Fatalf("expected unknown-library error, got %v", errs)
	}
}
