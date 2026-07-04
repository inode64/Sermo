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
  services: [ @ROOT@/services ]
  runtime: /run/sermo
defaults:
  policy:
    cooldown: 5m
  stop_policy:
    graceful_timeout: 30s
    force_kill: false
`

func TestResolveMergesDefaultsServiceOverrides(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/apache.yml": `
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
		"services/apache-main.yml": `
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
		t.Errorf("max_actions = %v, want service 3", got)
	}
	stop := nested(t, resolved.Tree, "stop_policy")
	if got := cfgval.String(stop["graceful_timeout"]); got != "30s" {
		t.Errorf("graceful_timeout = %v, want default 30s", got)
	}
}

func TestResolveDryRunDefaultsTargets(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
engine:
  backend: auto
paths:
  catalog: [ @ROOT@/catalog ]
  services: [ @ROOT@/services ]
  storages: [ @ROOT@/storages ]
  runtime: /run/sermo
defaults:
  dry_run: true
  policy: { cooldown: 5m }
watches:
  load-live:
    dry_run: false
    check:
      type: load
      load1: { op: ">", value: 10 }
    then: { notify: [none] }
`,
		"catalog/services/demo.yml": `
name: demo
service: demo
`,
		"services/demo.yml": `
name: demo
uses: demo
`,
		"storages/data.yml": `
name: data
path: /data
capacity:
  used_pct: { op: ">=", value: "90%" }
`,
	})

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	svc, errs := cfg.Resolve("demo")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if !DryRun(svc.Tree) {
		t.Fatal("service should inherit defaults.dry_run")
	}
	storage, errs := cfg.ResolveStorage("data")
	if len(errs) != 0 {
		t.Fatalf("ResolveStorage() errors = %v", errs)
	}
	if !DryRun(storage.Tree) {
		t.Fatal("storage should inherit defaults.dry_run")
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches() errors = %v", errs)
	}
	watch, ok := watches["load-live"].(map[string]any)
	if !ok {
		t.Fatalf("watch not resolved: %v", watches)
	}
	if DryRun(watch) {
		t.Fatal("watch dry_run false should override defaults.dry_run")
	}
	capacity, ok := watches["data"].(map[string]any)
	if !ok {
		t.Fatalf("storage capacity watch not resolved: %v", watches)
	}
	if !DryRun(capacity) {
		t.Fatal("storage capacity watch should inherit storage/defaults dry_run")
	}
}

func TestCatalogAliasResolvesUsesAndCatalogLookup(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/smb.yml": `
name: smb
aliases: [samba]
service: smb
`,
		"services/files.yml": `
name: files
uses: samba
`,
	})

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	catalog, errs := cfg.ResolveCatalog(CategoryService, "samba")
	if len(errs) != 0 {
		t.Fatalf("ResolveCatalog() errors = %v", errs)
	}
	if catalog.Name != "smb" {
		t.Fatalf("ResolveCatalog() name = %q, want smb", catalog.Name)
	}

	resolved, errs := cfg.Resolve("files")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if got := ServiceUnit(resolved.Tree, resolved.Name); got != "smb" {
		t.Fatalf("service unit = %q, want smb", got)
	}
}

func TestServiceAliasesResolveToCanonicalName(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/smb.yml": `
name: smb
aliases: [samba]
service: smb
`,
		"services/smb.yml": `
name: smb
aliases: [fileshare]
uses: smb
`,
	})

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, alias := range []string{"fileshare", "samba"} {
		canonical, ok := cfg.CanonicalServiceName(alias)
		if !ok {
			t.Fatalf("CanonicalServiceName(%q) was not found", alias)
		}
		if canonical != "smb" {
			t.Fatalf("CanonicalServiceName(%q) = %q, want smb", alias, canonical)
		}
		resolved, errs := cfg.Resolve(alias)
		if len(errs) != 0 {
			t.Fatalf("Resolve(%q) errors = %v", alias, errs)
		}
		if resolved.Name != "smb" {
			t.Fatalf("Resolve(%q) name = %q, want smb", alias, resolved.Name)
		}
	}
}

func TestCatalogAliasDoesNotResolveNonCanonicalServiceInstance(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/smb.yml": `
name: smb
aliases: [samba]
service: smb
`,
		"services/files.yml": `
name: files
uses: smb
`,
	})

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if canonical, ok := cfg.CanonicalServiceName("samba"); ok {
		t.Fatalf("CanonicalServiceName(samba) = %q, want no match", canonical)
	}
	if _, errs := cfg.Resolve("samba"); len(errs) == 0 {
		t.Fatalf("Resolve(samba) succeeded, want unknown service")
	}
}

func TestValidateDocumentAliases(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/web.yml": `
name: web
aliases: [web]
`,
		"catalog/services/db.yml": `
name: db
aliases: [bad/name]
`,
		"catalog/services/cache.yml": `
name: cache
aliases: ["", alt, alt]
`,
		"catalog/services/api.yml": `
name: api
aliases: [alt]
`,
		"catalog/services/plain.yml": `
name: plain
aliases: nope
`,
	})

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	for _, want := range []string{
		`alias "web" duplicates the document name`,
		`alias "bad/name" must be a simple name without path separators`,
		"aliases must not contain empty names",
		`duplicate alias "alt"`,
		`alias "alt" is already used by catalog service`,
		"aliases must be a list of simple names",
	} {
		if !hasIssue(issues, want) {
			t.Errorf("missing issue containing %q in %v", want, issues)
		}
	}
}

func TestCloneOverridesVariableBeforeExpansion(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/redis-main.yml": `
name: redis-main
variables:
  port: 6379
checks:
  ping:
    type: tcp
    port: "${port}"
`,
		"services/redis-cache.yml": `
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

func TestMultiInstanceServiceOverridesPerInstance(t *testing.T) {
	// Two services share one catalog service (same binary, checks and rules) but each
	// overrides only the variables that make an instance unique: listen port,
	// pidfile and config path. This is the supported pattern for running e.g.
	// two MariaDB or php-fpm instances off a single catalog service — no new mechanism
	// is needed beyond `uses` + per-instance `variables`.
	cfg, err := Load(writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/dbserver.yml": `
name: dbserver
service:
  systemd: [dbserver]
variables:
  host: 127.0.0.1
  port: 3306
  pidfile: /run/dbserver/main.pid
  config: /etc/dbserver/main.cnf
pidfile: "${pidfile}"
checks:
  tcp:
    type: tcp
    host: "${host}"
    port: "${port}"
  config:
    type: command
    command: ["dbserverd", "--defaults-file=${config}", "--help"]
`,
		"services/db-inst1.yml": `
name: db-inst1
uses: dbserver
service: db-inst1
variables:
  port: 3306
  pidfile: /run/dbserver/inst1.pid
  config: /etc/dbserver/inst1.cnf
`,
		"services/db-inst2.yml": `
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
		if got := cfgval.String(resolved.Tree["pidfile"]); got != w.pidfile {
			t.Errorf("%s pidfile = %q, want %q", name, got, w.pidfile)
		}
		if got := cfgval.String(nested(t, resolved.Tree, "checks", "pidfile")["path"]); got != w.pidfile {
			t.Errorf("%s checks.pidfile.path = %q, want %q", name, got, w.pidfile)
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
name: java
variables:
  binary: /usr/bin/java
preflight:
  binary: { type: binary, path: "${binary}" }
  health: { type: command, command: ["${binary}", "-help"] }
  version: { type: command, command: ["${binary}", "-version"] }
`,
		"catalog/services/tomcat.yml": `
name: tomcat
apps: [java]
variables:
  port: 8080
  binary: /opt/tomcat/bin/catalina.sh
preflight:
  binary: { type: binary, path: "${binary}" }
checks:
  port: { type: tcp, port: "${port}" }
`,
		"services/tomcat-main.yml": `
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

func TestAppsLinkUsesCanonicalAppName(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/dbus.yml": `
name: dbus
variables:
  binary: /usr/bin/dbus-daemon
preflight:
  binary: { type: binary, path: "${binary}" }
`,
		"catalog/services/dbus.yml": `
name: dbus
apps: [dbus]
preflight:
  config: { type: command, command: ["${dbus_binary}", "--check"] }
checks:
  service: { type: service, expect: active }
`,
		"services/dbus-main.yml": `
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
	if got := cfgval.String(nested(t, pf, "dbus-binary")["path"]); got != "/usr/bin/dbus-daemon" {
		t.Fatalf("linked app binary path = %q, want /usr/bin/dbus-daemon", got)
	}
	configCmd, _ := nested(t, pf, "config")["command"].([]any)
	if got := fmt.Sprint(configCmd...); got != "/usr/bin/dbus-daemon--check" {
		t.Fatalf("linked app variable command = %v, want dbus binary", configCmd)
	}
	if names := cfg.CatalogNamesInCategory(CategoryApp); strings.Join(names, ",") != "dbus" {
		t.Fatalf("listed apps = %v, want dbus", names)
	}
}

func TestAppsExposeNamespacedVariables(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/cupsd.yml": `
name: cupsd
variables:
  cups_config: /usr/bin/cups-config
  binary: /usr/sbin/cupsd
preflight:
  binary: { type: binary, path: "${binary}" }
  cups-config: { type: binary, path: "${cups_config}" }
  version: { type: command, command: ["${binary}", "--version"] }
  api: { type: command, command: ["${cups_config}", "--api"], export: { api: { default: 10 }, empty: {} } }
`,
		"catalog/services/cups.yml": `
name: cups
apps: [cupsd]
preflight:
  config: { type: command, command: ["${cupsd_binary}", "-t"] }
  version: { type: command, command: ["${cupsd_cups_config}", "--version"] }
  app-vars: { type: command, command: ["printf", "${cupsd_version}", "${cupsd_version_short}", "${cupsd_api}", "${cupsd_empty}"] }
checks:
  service: { type: service, expect: active }
`,
		"services/cups.yml": `
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
	appVarsCmd, _ := nested(t, preflight, "app-vars")["command"].([]any)
	wantAppVarsCmd := []any{"printf", "", "", "10", ""}
	if !slices.Equal(appVarsCmd, wantAppVarsCmd) {
		t.Fatalf("app-vars command = %#v, want %#v", appVarsCmd, wantAppVarsCmd)
	}
}

func TestSingleAppExposesDefaultVariables(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/php-fpm.yml": `
name: php-fpm
variables:
  config: /etc/php-fpm.conf
  binary: /usr/bin/php-fpm
preflight:
  binary: { type: binary, path: "${binary}" }
`,
		"catalog/services/php-fpm.yml": `
name: php-fpm
apps: [php-fpm]
preflight:
  config: { type: command, command: ["${binary}", "--test", "--fpm-config", "${config}"] }
processes:
  main: { exe: "${binary}", user: root }
checks:
  service: { type: service, expect: active }
`,
		"services/php-fpm.yml": `
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
name: cupsd
variables:
  binary: /usr/sbin/cupsd
preflight:
  binary: { type: binary, path: "${binary}" }
`,
		"catalog/services/cups.yml": `
name: cups
apps: [cupsd]
variables: { cupsd_binary: /opt/cups/sbin/cupsd }
preflight:
  config: { type: command, command: ["${cupsd_binary}", "-t"] }
checks:
  service: { type: service, expect: active }
`,
		"services/cups.yml": `
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
name: php-fpm
variables:
  binary: /usr/bin/php-fpm
preflight:
  binary: { type: binary, path: "${binary}" }
`,
		"catalog/services/php-fpm.yml": `
name: php-fpm
apps: [php-fpm]
variables:
  binary: /opt/php/sbin/php-fpm
preflight:
  config: { type: command, command: ["${binary}", "--test"] }
checks:
  service: { type: service, expect: active }
`,
		"services/php-fpm.yml": `
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
		"catalog/services/web.yml": `
name: web
apps: [no-such-app]
variables: { port: 80 }
checks:
  port: { type: tcp, port: "${port}" }
`,
		"services/web-main.yml": `
name: web-main
uses: web
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !hasIssue(Validate(cfg), `apps references unknown app "no-such-app"`) {
		t.Fatalf("Validate() did not report unknown linked app")
	}
	_, errs := cfg.Resolve("web-main")
	if len(errs) == 0 {
		t.Fatal("linking an unknown app must error")
	}
}

func TestValidateServiceAppsLinkUnknownApp(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/web-main.yml": `
name: web-main
apps: [no-such-app]
service: web
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !hasIssue(Validate(cfg), `apps references unknown app "no-such-app"`) {
		t.Fatalf("Validate() did not report unknown service app link")
	}
}

func TestValidateServiceAppsLinkInvalidShape(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/web-main.yml": `
name: web-main
apps: [app, 7]
service: web
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !hasIssue(Validate(cfg), "apps must be a string or list of strings") {
		t.Fatalf("Validate() did not report invalid apps shape")
	}
}

func TestAppsLinkPreflightKeyCollisionErrors(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/shared.yml": `
name: shared
variables:
  binary: /usr/bin/shared
preflight:
  binary: { type: binary, path: "${binary}" }
`,
		"catalog/services/stack.yml": `
name: stack
apps: [shared, shared]
variables:
  port: 8080
  binary: /opt/stack/bin/stack
checks:
  port: { type: tcp, port: "${port}" }
`,
		"services/stack-main.yml": `
name: stack-main
uses: stack
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, errs := cfg.Resolve("stack-main")
	if len(errs) == 0 {
		t.Fatal("duplicate app preflight keys must error")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, `apps preflight key "shared-binary" would overwrite`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("errors = %v; want a preflight key collision error", errs)
	}

	// A manual preflight key must not be silently overwritten by an app check.
	global = writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/shared.yml": `
name: shared
variables:
  binary: /usr/bin/shared
preflight:
  binary: { type: binary, path: "${binary}" }
`,
		"catalog/services/stack.yml": `
name: stack
apps: [shared]
variables:
  binary: /opt/stack/bin/stack
  port: 8080
preflight:
  shared-binary: { type: binary, path: "/opt/stack/bin/stack" }
checks:
  port: { type: tcp, port: "${port}" }
`,
		"services/stack-main.yml": `
name: stack-main
uses: stack
`,
	})
	cfg, err = Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, errs = cfg.Resolve("stack-main")
	if len(errs) == 0 {
		t.Fatal("manual/app preflight key collision must error")
	}
	found = false
	for _, e := range errs {
		if strings.Contains(e, `apps preflight key "shared-binary" would overwrite`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("errors = %v; want a manual preflight collision error", errs)
	}
}

func TestAppsLinkCycleErrorsInsteadOfRecursing(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/app-a.yml": `
name: app-a
apps: [app-b]
variables:
  binary: /usr/bin/app-a
`,
		"catalog/apps/app-b.yml": `
name: app-b
apps: [app-a]
variables:
  binary: /usr/bin/app-b
`,
		"catalog/services/web.yml": `
name: web
apps: [app-a]
variables: { port: 80 }
checks:
  port: { type: tcp, port: "${port}" }
`,
		"services/web-main.yml": `
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
		t.Fatal("a cyclic apps: linkage must error, not recurse")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "apps cycle detected") {
			found = true
		}
	}
	if !found {
		t.Fatalf("errors = %v; want an 'apps cycle detected' error", errs)
	}
}

func TestValidateCleanConfig(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/redis-main.yml": `
name: redis-main
service: redis
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
	configDir := filepath.Join(root, "examples")
	serviceDir := filepath.Join(configDir, "services")
	catalogDir := filepath.Join(root, "catalog")
	catalogServicesDir := filepath.Join(catalogDir, "services")
	for _, d := range []string{serviceDir, catalogServicesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(catalogServicesDir, "redis.yml"), []byte(`
name: redis
variables: { port: 6379 }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "redis-main.yml"), []byte(`
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
  services: [services]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
watches:
  disk:
    enabled: false
    check: { type: storage, path: /, used_pct: { op: ">=", value: 90 } }
    then:
      hook: { command: [/bin/true] }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.Global.Services[0]; got != serviceDir {
		t.Fatalf("Services[0] = %q, want %q", got, serviceDir)
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

func TestDefaultServiceAndAppDirs(t *testing.T) {
	wantServices := []string{"/etc/sermo/services"}
	gotServices := defaultConfigDirs(DefaultGlobalPath, defaultServiceDirs)
	if strings.Join(gotServices, "\n") != strings.Join(wantServices, "\n") {
		t.Fatalf("default service dirs = %v, want %v", gotServices, wantServices)
	}
	wantApps := []string{"/etc/sermo/apps"}
	gotApps := defaultConfigDirs(DefaultGlobalPath, defaultAppDirs)
	if strings.Join(gotApps, "\n") != strings.Join(wantApps, "\n") {
		t.Fatalf("default app dirs = %v, want %v", gotApps, wantApps)
	}
}

func TestLoadUsesConfigRelativeDefaultServiceDirsWhenServiceDirsOmitted(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  catalog: [ @ROOT@/catalog ]
  runtime: /run/sermo
`,
		"services/web.yml": `
name: web
`,
	})

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	root := filepath.Dir(global)
	wantServices := []string{filepath.Join(root, "services")}
	if got := strings.Join(cfg.Global.Services, "\n"); got != strings.Join(wantServices, "\n") {
		t.Fatalf("Global.Services = %v, want %v", cfg.Global.Services, wantServices)
	}
	if _, ok := cfg.Services["web"]; !ok {
		t.Fatalf("service from default services include was not loaded")
	}
}

func TestLoadUsesConfigRelativeDefaultStorageDirsWhenStoragesOmitted(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  catalog: [ @ROOT@/catalog ]
  runtime: /run/sermo
`,
		"storages/data.yml": `
name: mount-data
path: /data
mount: {}
`,
	})
	root := filepath.Dir(global)
	wantStorages := []string{filepath.Join(root, "storages")}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := strings.Join(cfg.Global.Storages, "\n"); got != strings.Join(wantStorages, "\n") {
		t.Fatalf("Global.Storages = %v, want %v", cfg.Global.Storages, wantStorages)
	}
	if _, ok := cfg.Storages["mount-data"]; !ok {
		t.Fatalf("storage from default storages dir was not loaded")
	}
}

func TestLoadPathSpecsRecursiveOptIn(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  catalog:
    - path: @ROOT@/catalog-flat
    - path: @ROOT@/catalog-recursive
      recursive: true
  services:
    - path: @ROOT@/services-flat
    - path: @ROOT@/services-recursive
      recursive: true
  apps:
    - path: @ROOT@/apps-flat
    - path: @ROOT@/apps-recursive
      recursive: true
  notifiers:
    - path: @ROOT@/notifiers-flat
    - path: @ROOT@/notifiers-recursive
      recursive: true
  storages:
    - path: @ROOT@/storages-flat
    - path: @ROOT@/storages-recursive
      recursive: true
  networks:
    - path: @ROOT@/networks-flat
    - path: @ROOT@/networks-recursive
      recursive: true
  watches:
    - path: @ROOT@/watches-flat
    - path: @ROOT@/watches-recursive
      recursive: true
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
notify: [ops]
`,
		"catalog-flat/services/direct-service.yml": `
name: direct-service
`,
		"catalog-flat/services/deep/skipped-service.yml": `
name: skipped-service
`,
		"catalog-recursive/services/deep/recursive-service.yml": `
name: recursive-service
`,
		"services-flat/direct-service.yml": `
name: direct-service
service: direct-service
`,
		"services-flat/deep/skipped-service.yml": `
name: skipped-service
service: skipped-service
`,
		"services-recursive/deep/recursive-service.yml": `
name: recursive-service
service: recursive-service
`,
		"apps-flat/direct-app.yml": `
name: direct-app
variables: { binary: /bin/true }
`,
		"apps-flat/deep/skipped-app.yml": `
name: skipped-app
variables: { binary: /bin/true }
`,
		"apps-recursive/deep/recursive-app.yml": `
name: recursive-app
variables: { binary: /bin/true }
`,
		"notifiers-flat/ops.yml": `
notifiers:
  ops:
    enabled: false
    type: email
`,
		"notifiers-flat/deep/skipped-notifier.yml": `
notifiers:
  skipped-notifier:
    enabled: false
    type: email
`,
		"notifiers-recursive/deep/team.yml": `
notifiers:
  team:
    enabled: false
    type: email
`,
		"storages-flat/root.yml": `
name: storage-direct
path: /
capacity:
  used_pct: { op: ">=", value: "90%" }
  then: { notify: [ops] }
`,
		"storages-flat/deep/skipped.yml": `
name: storage-skipped
path: /tmp
capacity:
  used_pct: { op: ">=", value: "90%" }
  then: { notify: [ops] }
`,
		"storages-recursive/deep/root.yml": `
name: storage-recursive
path: /var
capacity:
  used_pct: { op: ">=", value: "90%" }
  then: { notify: [ops] }
`,
		"networks-flat/ping.yml": `
name: network-direct
category: network
check: { type: icmp, host: 192.0.2.1 }
metrics:
  state:
    expect: up
    then: { notify: [ops] }
`,
		"networks-flat/deep/skipped.yml": `
name: network-skipped
check: { type: icmp, host: 192.0.2.2 }
metrics:
  state:
    expect: up
    then: { notify: [ops] }
`,
		"networks-recursive/deep/ping.yml": `
name: network-recursive
check: { type: icmp, host: 192.0.2.3 }
metrics:
  state:
    expect: up
    then: { notify: [ops] }
`,
		"watches-flat/load.yml": `
name: load-direct
check: { type: load, load5: { op: ">", value: 2 } }
then: { notify: [ops] }
`,
		"watches-flat/deep/skipped.yml": `
name: load-skipped
check: { type: load, load5: { op: ">", value: 3 } }
then: { notify: [ops] }
`,
		"watches-recursive/deep/load.yml": `
name: load-recursive
check: { type: load, load5: { op: ">", value: 4 } }
then: { notify: [ops] }
`,
		"storages-flat/direct-mount.yml": `
name: direct-mount
path: /mnt/direct
mount: {}
`,
		"storages-flat/deep/skipped-mount.yml": `
name: skipped-mount
path: /mnt/skipped
mount: {}
`,
		"storages-recursive/deep/recursive-mount.yml": `
name: recursive-mount
path: /mnt/recursive
mount: {}
`,
	})

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	for _, name := range []string{"direct-service", "recursive-service"} {
		if _, ok := cfg.CatalogServices[name]; !ok {
			t.Fatalf("catalog service %q was not loaded", name)
		}
	}
	if _, ok := cfg.CatalogServices["skipped-service"]; ok {
		t.Fatalf("non-recursive catalog path loaded nested catalog service")
	}
	for _, name := range []string{"direct-service", "recursive-service"} {
		if _, ok := cfg.Services[name]; !ok {
			t.Fatalf("service %q was not loaded", name)
		}
	}
	if _, ok := cfg.Services["skipped-service"]; ok {
		t.Fatalf("non-recursive services path loaded nested service")
	}
	for _, name := range []string{"direct-app", "recursive-app"} {
		if _, ok := cfg.Apps[name]; !ok {
			t.Fatalf("app %q was not loaded", name)
		}
	}
	if _, ok := cfg.Apps["skipped-app"]; ok {
		t.Fatalf("non-recursive apps path loaded nested app")
	}
	notifiers := cfg.Notifiers()
	for _, name := range []string{"ops", "team"} {
		if _, ok := notifiers[name]; !ok {
			t.Fatalf("notifier %q was not loaded: %v", name, notifiers)
		}
	}
	if _, ok := notifiers["skipped-notifier"]; ok {
		t.Fatalf("non-recursive notifiers path loaded nested notifier")
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches() errors: %v", errs)
	}
	for _, name := range []string{"storage-direct", "storage-recursive", "network-direct", "network-recursive", "load-direct", "load-recursive"} {
		if _, ok := watches[name]; !ok {
			t.Fatalf("watch %q was not loaded: %v", name, watches)
		}
	}
	if got := watches["network-direct"].(map[string]any)["category"]; got != "network" {
		t.Fatalf("included network watch category = %v, want network", got)
	}
	for _, name := range []string{"storage-skipped", "network-skipped", "load-skipped"} {
		if _, ok := watches[name]; ok {
			t.Fatalf("non-recursive watch path loaded nested watch %q", name)
		}
	}
	for _, name := range []string{"direct-mount", "recursive-mount"} {
		if _, ok := cfg.Storages[name]; !ok {
			t.Fatalf("mount-capable storage %q was not loaded", name)
		}
	}
	if _, ok := cfg.Storages["skipped-mount"]; ok {
		t.Fatalf("non-recursive storages path loaded nested mount-capable storage")
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("recursive path config should validate, got %v", issues)
	}
}

func TestLoadRelativeConfigPathResolvesDirsAbsolute(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "conf", "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "sermo.yml"), []byte(`
paths:
  services: [services]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "services", "web.yml"), []byte(`
name: web
service: web
`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(root)
	cfg, err := Load(filepath.Join("conf", "sermo.yml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := filepath.Join(root, "conf", "services")
	if got := cfg.Global.Services; len(got) != 1 || got[0] != want {
		t.Fatalf("Global.Services = %v, want [%s]", got, want)
	}
	if _, ok := cfg.Services["web"]; !ok {
		t.Fatalf("relative service directory was not loaded: %v", cfg.ServiceNames)
	}
}

func TestStorageCapacityWatchRejectsDuplicate(t *testing.T) {
	root := t.TempDir()
	storages := filepath.Join(root, "storages")
	if err := os.MkdirAll(storages, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(`
paths:
  storages: [storages]
watches:
  storage-root:
    check: { type: storage, path: /, used_pct: { op: ">=", value: 90 } }
    then:
      hook: { command: [/bin/true] }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storages, "storage-root.yml"), []byte(`
name: storage-root
path: /
capacity:
  used_pct: { op: ">=", value: 95 }
  then:
    hook: { command: [/bin/true] }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if issues := Validate(cfg); !hasIssue(issues, `capacity watch would overwrite existing watch "storage-root"`) {
		t.Fatalf("Validate issues = %v, want duplicate generated watch", issues)
	}
}

func TestLoadIncludedWatchDocumentRejectsDuplicate(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  watches: [ @ROOT@/watches ]
defaults:
  policy: { cooldown: 5m }
watches:
  load:
    check: { type: load, load5: { op: ">", value: 2 } }
`,
		"watches/load.yml": `
name: load
check: { type: load, load5: { op: ">", value: 3 } }
`,
	})

	if _, err := Load(global); err == nil || !strings.Contains(err.Error(), `watch "load" is already defined`) {
		t.Fatalf("Load() error = %v, want duplicate watch", err)
	}
}

func TestLoadIncludedWatchDocumentRequiresName(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  watches: [ @ROOT@/watches ]
defaults:
  policy: { cooldown: 5m }
`,
		"watches/load.yml": `
check: { type: load, load5: { op: ">", value: 3 } }
`,
	})

	if _, err := Load(global); err == nil || !strings.Contains(err.Error(), "watch documents must define name") {
		t.Fatalf("Load() error = %v, want missing watch name", err)
	}
}

func TestLoadIncludedWatchDocumentRejectsGroupedWatchesMap(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  watches: [ @ROOT@/watches ]
defaults:
  policy: { cooldown: 5m }
`,
		"watches/load.yml": `
watches:
  load:
    check: { type: load, load5: { op: ">", value: 3 } }
`,
	})

	if _, err := Load(global); err == nil || !strings.Contains(err.Error(), "watch documents use top-level name/check fields, not a watches map") {
		t.Fatalf("Load() error = %v, want grouped watches map rejection", err)
	}
}

func TestStorageMountCapacityDefaultsMounted(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  storages: [ @ROOT@/storages ]
`,
		"storages/backup.yml": `
name: storage-backup
path: /mnt/backup
capacity:
  used_pct: { op: ">=", value: 90 }
mount:
  refcount: true
`,
		"storages/archive.yml": `
name: storage-archive
path: /mnt/archive
capacity:
  mounted: false
mount:
  refcount: true
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches() errors: %v", errs)
	}
	backup := watches["storage-backup"].(map[string]any)["check"].(map[string]any)
	if backup["mounted"] != true {
		t.Fatalf("storage-backup mounted = %v, want true", backup["mounted"])
	}
	archive := watches["storage-archive"].(map[string]any)["check"].(map[string]any)
	if archive["mounted"] != false {
		t.Fatalf("storage-archive mounted = %v, want explicit false", archive["mounted"])
	}
}

func TestStorageCapacityWatchKeepsMetadata(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  storages: [ @ROOT@/storages ]
defaults:
  policy: { cooldown: 5m }
`,
		"storages/root.yml": `
name: storage-root
display_name: Root filesystem
description: System volume
category: storage
path: /
capacity:
  used_pct: { op: ">=", value: 90 }
`,
	})

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches() errors = %v", errs)
	}
	entry, ok := watches["storage-root"].(map[string]any)
	if !ok {
		t.Fatalf("storage-root watch = %v, want mapping", watches["storage-root"])
	}
	if got := cfgval.String(entry["display_name"]); got != "Root filesystem" {
		t.Fatalf("display_name = %q, want Root filesystem", got)
	}
	if got := cfgval.String(entry["description"]); got != "System volume" {
		t.Fatalf("description = %q, want System volume", got)
	}
	if got := cfgval.String(entry["category"]); got != "storage" {
		t.Fatalf("category = %q, want storage", got)
	}
}

func TestLoadIncludedNotifierFragments(t *testing.T) {
	t.Setenv("SMTP_DSN", "smtp://user:pw@mail.example.com:587")
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  notifiers: [ @ROOT@/notifiers ]
  storages: [ @ROOT@/storages ]
defaults:
  policy: { cooldown: 5m }
notify: [ops]
`,
		"notifiers/ops.yml": `
notifiers:
  ops:
    type: email
    dsn: "${env:SMTP_DSN}"
    from: "Sermo <sermo@example.com>"
    to: [ops@example.com]
`,
		"storages/storage-root.yml": `
name: storage-root
path: /
capacity:
  used_pct: { op: ">=", value: "90%" }
  then:
    notify: [ops]
`,
	})

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	notifier := cfg.Notifiers()["ops"].(map[string]any)
	if notifier["dsn"] != "smtp://user:pw@mail.example.com:587" {
		t.Fatalf("included notifier env not expanded: %v", notifier["dsn"])
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches() errors: %v", errs)
	}
	if _, ok := watches["storage-root"]; !ok {
		t.Fatalf("storage watch not generated: %v", watches)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("included notifier/watch config should validate, got %v", issues)
	}
}

func TestLoadIncludedNotifierFragmentRejectsDuplicate(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  notifiers: [ @ROOT@/notifiers ]
defaults:
  policy: { cooldown: 5m }
notifiers:
  ops:
    enabled: false
    type: email
`,
		"notifiers/ops.yml": `
notifiers:
  ops:
    enabled: false
    type: email
`,
	})

	if _, err := Load(global); err == nil || !strings.Contains(err.Error(), `notifier "ops" is already defined`) {
		t.Fatalf("Load() error = %v, want duplicate notifier", err)
	}
}

func TestLoadIncludedNotifierFragmentRequiresSingleEntry(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  notifiers: [ @ROOT@/notifiers ]
defaults:
  policy: { cooldown: 5m }
`,
		"notifiers/multi.yml": `
notifiers:
  ops:
    enabled: false
    type: email
  pager:
    enabled: false
    type: email
`,
	})

	if _, err := Load(global); err == nil || !strings.Contains(err.Error(), "notifiers fragments must contain exactly one entry") {
		t.Fatalf("Load() error = %v, want one-notifier-per-file error", err)
	}
}

func TestLoadExplicitTargetDirectories(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
paths:
  services: [ @ROOT@/services ]
  notifiers: [ @ROOT@/notifiers ]
  storages: [ @ROOT@/storages ]
  networks: [ @ROOT@/networks ]
  watches: [ @ROOT@/watches ]
defaults:
  policy: { cooldown: 5m }
notify: [ops]
`,
		"services/web.yml": `
name: web
service: web
checks:
  service: { type: service, expect: active }
`,
		"notifiers/ops.yml": `
notifiers:
  ops:
    enabled: false
    type: email
`,
		"storages/root.yml": `
name: storage-root
path: /
capacity:
  used_pct: { op: ">=", value: "90%" }
  then: { notify: [ops] }
`,
		"storages/backup.yml": `
name: backup
path: /mnt/backup
mount: {}
`,
		"networks/ping.yml": `
name: ping-gw
category: network
check: { type: icmp, host: 8.8.8.8 }
metrics:
  state:
    expect: up
    then: { notify: [ops] }
`,
		"watches/load.yml": `
name: load
category: host
check: { type: load, load5: { op: ">", value: 2 } }
then: { notify: [ops] }
`,
	})

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.Services["web"]; !ok {
		t.Fatalf("service directory was not loaded: %v", cfg.ServiceNames)
	}
	if _, ok := cfg.Notifiers()["ops"]; !ok {
		t.Fatalf("notifier directory was not loaded: %v", cfg.Notifiers())
	}
	watches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("ResolveWatches() errors: %v", errs)
	}
	for _, name := range []string{"storage-root", "ping-gw", "load"} {
		if _, ok := watches[name]; !ok {
			t.Fatalf("watch %q was not loaded from explicit directories: %v", name, watches)
		}
	}
	if got := cfgval.String(watches["ping-gw"].(map[string]any)["category"]); got != "network" {
		t.Fatalf("included watch category = %q, want network", got)
	}
	if _, ok := cfg.Storages["backup"]; !ok {
		t.Fatalf("mount-capable storage directory was not loaded: %v", cfg.StorageNames)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("explicit target directory config should validate, got %v", issues)
	}
}

func TestValidateGlobalErrors(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
engine:
  backend: bogus
paths:
  catalog: [ @ROOT@/catalog ]
  services: [ @ROOT@/services ]
  locks: /run/sermo/locks
  runtime: relative/path
  templates: relative/templates
  unexpected: /tmp/sermo
defaults:
  mystery: true
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
		"paths.locks is not supported; runtime locks derive from paths.runtime",
		"paths.runtime",
		"paths.templates",
		"paths.unexpected is not supported",
		"security.allow_sigkill_by_default",
		"defaults.mystery is not supported",
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
paths: { services: [ @ROOT@/services ] }
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
paths: { services: [ @ROOT@/services ] }
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
paths: { services: [ @ROOT@/services ] }
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
		"services/bad.yml": `
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
		"services/a.yml": `
name: a
clone: b
`,
		"services/b.yml": `
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
		"services/nested.yml": `
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

func TestPreflightBinarySelectsExecutableCandidate(t *testing.T) {
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
		"catalog/services/app.yml": `
name: app
variables:
  binary:
    - ` + notExec + `
    - ` + execPath + `
preflight:
  binary: { type: binary, path: "${binary}" }
checks:
  process: { type: process, exe: "${binary}", user: root }
`,
		"services/app-main.yml": "name: app-main\nuses: app\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("app-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
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

func TestLibraryBinaryVariableIsPlainVariable(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/libs/libdemo.yml": `
name: libdemo
variables:
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

func TestPidfileRejectsRelativeCandidate(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/app.yml": `
name: app
pidfile: run/app.pid
`,
		"services/app-main.yml": "name: app-main\nuses: app\n",
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
		"catalog/services/db.yml": `
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
		// Inherits the catalog service's display_name; name is its own.
		"services/db-main.yml": `
name: db-main
uses: db
service: db
`,
		// No display_name anywhere: ${display_name} must fall back to name.
		"services/plain.yml": `
name: plain
service: plain
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
		"services/custom.yml": `
name: custom
service: custom
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
		"services/plain.yml": `
name: plain
service: plain
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
		"services/one.yml": `
name: dup
`,
		"services/two.yml": `
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
		"services/bad.yml": `
name: ../escape
service: mysql
`,
		"catalog/services/bad.yml": `
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
		t.Fatalf("missing catalog service name issue in %v", issues)
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
		"services/web.yml": `
name: web
service: nginx
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
		"services/db.yml": `
name: db
service: postgresql
pidfile: "${pidfile}"
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
	if got := cfgval.String(resolved.Tree["pidfile"]); got != "/run/postgresql.pid" {
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
		"services/db.yml": `
name: db
service: postgresql
variables:
  user: postgres
  pidfile: /run/postgresql/main.pid
pidfile: "${pidfile}"
checks:
  who: { type: command, command: ["id", "${user}"] }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, _ := cfg.Resolve("db")
	if got := cfgval.String(resolved.Tree["pidfile"]); got != "/run/postgresql/main.pid" {
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
		"services/web.yml": `
name: web
service: web
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
		"services/db.yml": `
name: db
service: db
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
		"services/db.yml": `
name: db
service: db
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
		"services/db.yml": `
name: db
service: db
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
		"catalog/services/apache.yml": `
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
	body := cfg.CatalogServices["apache"].Body

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
		"catalog/services/db.yml": `
name: db
pidfile:
  os:
    gentoo: [/run/db1.pid, /run/db.pid]
    default: [/run/db.pid]
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	got, _ := cfg.CatalogServices["db"].Body["pidfile"].([]any)
	if len(got) != 2 || got[0] != "/run/db1.pid" {
		t.Errorf("pidfile = %v, want the gentoo candidate list", cfg.CatalogServices["db"].Body["pidfile"])
	}
}

func TestOSVariableBaked(t *testing.T) {
	old := detectedOS
	detectedOS = "debian"
	defer func() { detectedOS = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/app.yml": `
name: app
variables:
  binary: "/opt/${os}/bin/app"
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := DocumentBinary(cfg.CatalogServices["app"].Body); got != "/opt/debian/bin/app" {
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
		"catalog/services/qemu.yml": `
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
	if got := DocumentBinary(cfg.CatalogServices["qemu"].Body); got != "/usr/bin/qemu-system-aarch64" {
		t.Errorf("baked binary = %q, want /usr/bin/qemu-system-aarch64", got)
	}
	resolved, errs := cfg.ResolveCatalogService("qemu")
	if len(errs) != 0 {
		t.Fatalf("ResolveCatalogService() errors = %v", errs)
	}
	bin := nested(t, resolved.Tree, "preflight", "binary")
	if cfgval.String(bin["path"]) != "/usr/bin/qemu-system-aarch64" {
		t.Errorf("resolved binary path = %v, want /usr/bin/qemu-system-aarch64", bin["path"])
	}
}

func TestCatalogCategoryFromDirectory(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml":                   baseGlobal,
		"catalog/services/nginx.yml":  "name: nginx\nservice: nginx\n",
		"catalog/apps/git.yml":        "name: git\nbinary: /usr/bin/git\n",
		"catalog/libs/glibc.yml":      "name: glibc\nbinary: /lib64/libc.so.6\n",
		"catalog/patterns/common.yml": "name: common\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cases := []struct {
		name, wantCat string
		reg           map[string]*Document
	}{
		{"nginx", CategoryService, cfg.CatalogServices},
		{"git", CategoryApp, cfg.Apps},
		{"glibc", CategoryLibrary, cfg.Libraries},
		{"common", CategoryPatterns, cfg.Patterns},
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
	if got := cfg.CatalogNamesInCategory(CategoryApp); len(got) != 1 || got[0] != "git" {
		t.Errorf("CatalogNamesInCategory(app) = %v, want [git]", got)
	}
}

func TestCatalogRootFilesRejected(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml":         baseGlobal,
		"catalog/nginx.yml": "name: nginx\nservice: nginx\n",
	})
	_, err := Load(global)
	if err == nil || !strings.Contains(err.Error(), "catalog documents must live under services, apps, libs, or patterns") {
		t.Fatalf("Load() error = %v, want catalog root rejection", err)
	}
}

func TestReloadOnChangeDesugarsToReloadRule(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/udev.yml": `
name: udev
service: systemd-udevd
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
name: glibc
display_name: "GNU C Library"
variables:
  binary: "/lib64/libc.so.6"
`,
		"services/web.yml": `
name: web
service: web
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
		// nginx is a catalog service, not a library: referencing it must error.
		"catalog/services/nginx.yml": "name: nginx\nservice: nginx\n",
		"services/web.yml": `
name: web
service: web
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

func TestChangedAppVersionRuleValidatesResolvedVersionCommand(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/containerd.yml": `
name: containerd
preflight:
  version:
    type: command
    command: ["/usr/bin/containerd", "--version"]
    timeout: 5s
`,
		"services/containerd.yml": `
name: containerd
service: containerd
apps: [containerd]
rules:
  restart-if-containerd-version-changed:
    type: remediation
    if:
      changed:
        app: containerd
        level: patch
    then:
      action: restart
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate() issues = %v, want none", issues)
	}
	resolved, errs := cfg.Resolve("containerd")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	changed := nested(t, resolved.Tree, "rules", "restart-if-containerd-version-changed", "if", "changed")
	if got := cfgval.String(changed["app"]); got != "containerd" {
		t.Fatalf("changed.app = %q, want containerd", got)
	}
	if cfgval.String(nested(t, resolved.Tree, "preflight", "containerd-version")["type"]) != "command" {
		t.Fatal("resolved service must expose containerd-version preflight for changed.app")
	}
}

func TestChangedAppVersionRuleRequiresVersionCommand(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/containerd.yml": `
name: containerd
preflight:
  health:
    type: command
    command: ["/usr/bin/containerd", "--help"]
    timeout: 5s
`,
		"services/containerd.yml": `
name: containerd
service: containerd
apps: [containerd]
rules:
  restart-if-containerd-version-changed:
    type: remediation
    if: { changed: { app: containerd, level: patch } }
    then: { action: restart }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !hasIssue(Validate(cfg), `changed app "containerd" has no app version command`) {
		t.Fatalf("Validate() did not reject changed.app without a version command")
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

func TestMaterializedTemplateMatchesUsesAllBinaryCandidates(t *testing.T) {
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
	got := materializedTemplateMatches(paths, false, nil, []tmplToken{*tok})
	want := []string{"8.2", "8.3"}
	if values := templateMatchValues(got, "version"); strings.Join(values, ",") != strings.Join(want, ",") {
		t.Fatalf("materializedTemplateMatches = %v, want %v", values, want)
	}
}

func TestMaterializedTemplateMatchesDedupesSameTupleAcrossSources(t *testing.T) {
	root := t.TempDir()
	etcSystemdDir := filepath.Join(root, "etc", "systemd", "system")
	libSystemdDir := filepath.Join(root, "usr", "lib", "systemd", "system")
	if err := os.MkdirAll(etcSystemdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(libSystemdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{
		filepath.Join(etcSystemdDir, "php-fpm@8.2.service"),
		filepath.Join(libSystemdDir, "php-fpm@8.2.service"),
	} {
		if err := os.WriteFile(file, []byte("[Service]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	paths := []string{
		filepath.Join(etcSystemdDir, "php-fpm@${version}${sep}${instance}.service"),
		filepath.Join(libSystemdDir, "php-fpm@${version}${sep}${instance}.service"),
	}
	got := materializedTemplateMatches(paths, false, nil, tokensFor("php-fpm%v%s%i"))
	if len(got) != 1 {
		t.Fatalf("materializedTemplateMatches returned %d matches, want one: %#v", len(got), got)
	}
	if got[0].values["version"] != "8.2" || got[0].values["sep"] != "" || got[0].values["instance"] != "" {
		t.Fatalf("materializedTemplateMatches values = %v, want version 8.2 with empty sep/instance", got[0].values)
	}
	if got[0].matchedPath != filepath.Join(etcSystemdDir, "php-fpm@8.2.service") {
		t.Fatalf("materializedTemplateMatches kept %q, want first unit source", got[0].matchedPath)
	}
}

func TestMaterializedServiceUnitMatchesOptionalInstanceFromVersionCandidate(t *testing.T) {
	toks := tokensFor("php-fpm%v%s%i")
	patterns := serviceUnitPatternsForBackend("systemd", []string{
		"php-fpm${version}",
	}, toks)
	got := materializedServiceUnitMatches(patterns, []string{"php-fpm8.3.service"}, toks)
	if len(got) != 1 {
		t.Fatalf("materializedServiceUnitMatches returned %d matches, want one: %#v", len(got), got)
	}
	if got[0].values["version"] != "8.3" || got[0].values["sep"] != "" || got[0].values["instance"] != "" {
		t.Fatalf("materializedServiceUnitMatches values = %v, want version 8.3 with empty sep/instance", got[0].values)
	}
	if got[0].matchedPath != "php-fpm8.3.service" {
		t.Fatalf("materializedServiceUnitMatches matched path = %q", got[0].matchedPath)
	}
}

func TestVersionTemplateDiscoverySelectsActiveInitSources(t *testing.T) {
	root := t.TempDir()
	systemdDir := filepath.Join(root, "usr", "lib", "systemd", "system")
	openrcDir := filepath.Join(root, "etc", "init.d")
	for _, dir := range []string{systemdDir, openrcDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{
		filepath.Join(systemdDir, "svc@2.0.service"),
		filepath.Join(openrcDir, "svc-3.0"),
	} {
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	load := func(t *testing.T, backend string) *Config {
		t.Helper()
		catalogDir := filepath.Join(root, "catalog-"+backend, "services")
		servicesDir := filepath.Join(root, "enabled-"+backend)
		if err := os.MkdirAll(catalogDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(servicesDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(catalogDir, "svc.yml"), []byte(fmt.Sprintf(`
name: svc%%v
service: svc${version}
versions:
  from:
    systemd: %s/svc@${version}.service
    openrc: %s/svc-${version}
checks:
  service: { type: service, expect: active }
`, systemdDir, openrcDir)), 0o644); err != nil {
			t.Fatal(err)
		}
		global := filepath.Join(root, "sermo-"+backend+".yml")
		if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: %s }
paths: { catalog: [ %s ], services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, backend, filepath.Dir(catalogDir), servicesDir)), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(global)
		if err != nil {
			t.Fatalf("Load(%s): %v", backend, err)
		}
		return cfg
	}

	systemd := load(t, "systemd")
	if _, ok := systemd.CatalogServices["svc2.0"]; !ok {
		t.Fatal("systemd discovery missing svc2.0")
	}
	if _, ok := systemd.CatalogServices["svc3.0"]; ok {
		t.Fatal("systemd discovery must not use OpenRC versions.from")
	}
	if _, ok := systemd.CatalogServices["svc1.0"]; ok {
		t.Fatal("systemd discovery must not use a shared default versions.from branch")
	}

	openrc := load(t, "openrc")
	if _, ok := openrc.CatalogServices["svc3.0"]; !ok {
		t.Fatal("openrc discovery missing svc3.0")
	}
	if _, ok := openrc.CatalogServices["svc2.0"]; ok {
		t.Fatal("openrc discovery must not use systemd versions.from")
	}
	if _, ok := openrc.CatalogServices["svc1.0"]; ok {
		t.Fatal("openrc discovery must not use a shared default versions.from branch")
	}

	t.Run("env backend override", func(t *testing.T) {
		t.Setenv("SERMO_BACKEND", "openrc")
		cfg := load(t, "auto")
		if _, ok := cfg.CatalogServices["svc3.0"]; !ok {
			t.Fatal("SERMO_BACKEND=openrc should select OpenRC versions.from")
		}
		if _, ok := cfg.CatalogServices["svc2.0"]; ok {
			t.Fatal("SERMO_BACKEND=openrc must not select systemd versions.from")
		}
	})
}

func templateMatchValues(matches []templateMatch, variable string) []string {
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.values[variable])
	}
	return out
}

// TestCatalogServiceVersionTemplateDiscoversFromLinkedApp covers a catalog service template whose
// monitored binary is generic (no ${version}); installed versions come from the
// linked app template, and ${version} is baked into the service body.
func TestCatalogServiceVersionTemplateDiscoversFromLinkedApp(t *testing.T) {
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
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(catalogDir, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(catalogDir, "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	appTmpl := fmt.Sprintf(`
name: php-fpm%%v
display_name: "PHP-FPM ${version}"
versions:
  from: "%s/php${version}/bin/php-fpm"
variables:
  binary: "%s/php${version}/bin/php-fpm"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "-v"] }
`, slots, slots)
	if err := os.WriteFile(filepath.Join(catalogDir, "apps", "php-fpm.yml"), []byte(appTmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl := `
name: php-fpm%v
display_name: "PHP-FPM ${version}"
service:
  systemd: ["php${version}-fpm"]
apps: ["php-fpm${version}"]
variables:
  binary: /usr/sbin/php-fpm
`
	if err := os.WriteFile(filepath.Join(catalogDir, "services", "php-fpm.yml"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: openrc }
paths: { catalog: [ %s ], services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, catalogDir, servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, v := range []string{"7.4", "8.3"} {
		doc, ok := cfg.CatalogServices["php-fpm"+v]
		if !ok {
			t.Fatalf("expected materialized service php-fpm%s", v)
		}
		// Generic binary is preserved; version did not leak into it.
		if got := DocumentBinary(doc.Body); got != "/usr/sbin/php-fpm" {
			t.Errorf("php-fpm%s binary = %q, want /usr/sbin/php-fpm", v, got)
		}
		// ${version} baked into the service unit candidate.
		sysd := nested(t, doc.Body, "service")["systemd"].([]any)
		if got := sysd[0].(string); got != "php"+v+"-fpm" {
			t.Errorf("php-fpm%s service unit = %q, want php%s-fpm", v, got, v)
		}
		// Discovery metadata stripped from the concrete service.
		if _, present := doc.Body["versions"]; present {
			t.Errorf("php-fpm%s still carries versions block", v)
		}
	}
}

func TestCatalogServiceVersionTemplateRequiresLinkedAppDiscovery(t *testing.T) {
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
		"catalog/services/worker.yml": fmt.Sprintf(`
name: worker%%v
variables:
  binary: "%s/worker${version}"
checks: { service: { type: service, expect: active } }
`, bin),
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.CatalogServices["worker1.0"]; ok {
		t.Fatalf("service template materialized from service binary; expected linked app discovery only")
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
	servicesDir := filepath.Join(root, "services")
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
name: java
variables:
  binary: /usr/bin/java
preflight:
  binary: { type: binary, path: "${binary}" }
`)
	write(filepath.Join(catalogDir, "apps"), "tomcat.yml", fmt.Sprintf(`
name: tomcat-%%v
display_name: "Apache Tomcat ${version}"
variables:
  binary: %q
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "version"], timeout: 10s }
`, catalina))
	write(filepath.Join(catalogDir, "services"), "tomcat.yml", `
name: tomcat-%v
display_name: "Apache Tomcat ${version}"
service: tomcat
apps: [java, "tomcat-${version}"]
variables: { port: 8080 }
checks: { service: { type: service, expect: active } }
`)
	write(servicesDir, "site.yml", "name: site\nuses: tomcat-10\n")

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: systemd }
paths:
  catalog: [ %s ]
  services: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, catalogDir, servicesDir)), 0o644); err != nil {
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
		if _, ok := cfg.CatalogServices["tomcat-"+v]; !ok {
			t.Fatalf("expected materialized service tomcat-%s", v)
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
	servicesDir := filepath.Join(root, "services")
	write := func(dir, file, content string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(pgRoot, "postgresql-${version}", "bin", "postgres")
	write(filepath.Join(catalogDir, "apps"), "postgres.yml", fmt.Sprintf(`
name: postgres-%%v
display_name: "PostgreSQL ${version}"
variables:
  binary: %q
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "--version"], timeout: 10s }
`, binary))
	write(filepath.Join(catalogDir, "services"), "postgres.yml", `
name: postgres-%v
display_name: "PostgreSQL ${version}"
service: "postgresql-${version}"
apps: ["postgres-${version}"]
variables:
  port: 5432
  data_dir: /var/lib/postgresql/${version}/data
pidfile: "${data_dir}/postmaster.pid"
checks: { service: { type: service, expect: active } }
`)
	write(servicesDir, "pg.yml", "name: pg\nuses: postgres-16\n")

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths:
  catalog: [ %s ]
  services: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, catalogDir, servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global, WithServiceUnits("systemd", []string{"postgresql-16.service"}))
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
	}
	if _, ok := cfg.CatalogServices["postgres-16"]; !ok {
		t.Fatal("expected materialized service postgres-16 from active service unit")
	}
	if _, ok := cfg.CatalogServices["postgres-15"]; ok {
		t.Fatal("postgres-15 service must not materialize without an active service unit")
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
	if got := cfgval.String(resolved.Tree["pidfile"]); got != "/var/lib/postgresql/16/data/postmaster.pid" {
		t.Fatalf("postgres-16 pidfile = %q, want /var/lib/postgresql/16/data/postmaster.pid", got)
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
	catalogServicesDir := filepath.Join(catalogDir, "services")
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{appsDir, catalogServicesDir, servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(appsDir, "php-fpm.yml"), []byte(fmt.Sprintf(`
name: php-fpm%%v
display_name: "PHP-FPM ${version}"
variables:
  binary: "%s/php-fpm${version}"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "-v"] }
`, bin)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogServicesDir, "php-fpm.yml"), []byte(`
name: php-fpm%v
display_name: "PHP-FPM ${version}"
apps: ["php-fpm${version}"]
preflight:
  config: { type: command, command: ["${binary}", "--test"] }
processes:
  main: { exe: "${binary}", user: root }
checks:
  service: { type: service, expect: active }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, catalogDir, servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, version := range []string{"8.2", "8.4"} {
		name := "php-fpm" + version
		if _, ok := cfg.CatalogServices[name]; !ok {
			t.Fatalf("expected materialized service %s", name)
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
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{appsDir, servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(appsDir, "python%n.yml"), []byte(fmt.Sprintf(`
name: python%%n
display_name: "Python ${n}"
description: "Python runtime ${n}"
variables:
  binary: "%s/python${n}"
preflight:
  binary: { type: binary, path: "${binary}" }
`, bin)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appsDir, "php.yml"), []byte(fmt.Sprintf(`
name: php%%v
display_name: "PHP ${version}"
description: "PHP runtime ${version}"
variables:
  binary: "%s/php${version}"
preflight:
  binary: { type: binary, path: "${binary}" }
`, bin)), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, catalogDir, servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := strings.Join(cfg.CatalogNamesInCategory(CategoryApp), ","); got != "php,php8.4,python,python3" {
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
			if got := DocumentBinary(doc.Body); got != tt.binary {
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

func TestVersionTemplateCurrentMarker(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	sbin := filepath.Join(root, "sbin")
	for _, dir := range []string{bin, sbin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeBin := func(dir, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	linkBin := func(dir, link, target string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}

	for _, name := range []string{"php8.1", "php8.2", "python2", "python3"} {
		writeBin(bin, name)
	}
	for _, name := range []string{"php-fpm8.2", "php-fpm8.3"} {
		writeBin(sbin, name)
	}
	linkBin(bin, "php", "php8.2")
	linkBin(bin, "python", "python3")
	linkBin(sbin, "php-fpm", "php-fpm8.3")

	catalogDir := filepath.Join(root, "catalog")
	appsDir := filepath.Join(catalogDir, "apps")
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{appsDir, servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeApp := func(file, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(appsDir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeApp("php.yml", fmt.Sprintf(`
name: php%%v
display_name: "PHP ${version} ${current}"
variables:
  binary: "%s/php${version}"
`, bin))
	writeApp("php-fpm.yml", fmt.Sprintf(`
name: php-fpm%%v
display_name: "PHP-FPM ${version} ${current}"
variables:
  binary:
    - "%s/missing/php-fpm${version}"
    - "%s/php-fpm${version}"
`, root, sbin))
	writeApp("python%n.yml", fmt.Sprintf(`
name: python%%n
display_name: "Python ${n} ${current}"
variables:
  binary: "%s/python${n}"
`, bin))
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, catalogDir, servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	tests := []struct {
		name string
		want string
	}{
		{"php", "PHP"},
		{"php8.1", "PHP 8.1"},
		{"php8.2", "PHP 8.2 current"},
		{"php-fpm", "PHP-FPM"},
		{"php-fpm8.2", "PHP-FPM 8.2"},
		{"php-fpm8.3", "PHP-FPM 8.3 current"},
		{"python", "Python"},
		{"python2", "Python 2"},
		{"python3", "Python 3 current"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, ok := cfg.Apps[tt.name]
			if !ok {
				t.Fatalf("app %q was not materialized", tt.name)
			}
			if got := DisplayName(doc.Body, tt.name); got != tt.want {
				t.Fatalf("%s display_name = %q, want %q", tt.name, got, tt.want)
			}
			if resolved, errs := cfg.ResolveCatalog(CategoryApp, tt.name); len(errs) > 0 {
				t.Fatalf("ResolveCatalog(%s) = %+v, %v", tt.name, resolved, errs)
			}
		})
	}
}

func TestJavaVersionTemplateDiscoversFullVersionsFromJVMDirectory(t *testing.T) {
	root := t.TempDir()
	jvm := filepath.Join(root, "usr", "lib", "jvm")
	opt := filepath.Join(root, "opt")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{jvm, opt, bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeJavaHome := func(name, releaseVersion string) string {
		t.Helper()
		home := filepath.Join(opt, name)
		if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "bin", "java"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "release"), []byte(`JAVA_VERSION="`+releaseVersion+`"`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return home
	}
	java21 := writeJavaHome("openjdk-bin-21.0.11_p10", "21.0.11")
	java25 := writeJavaHome("openjdk-bin-25.0.3_p9", "25.0.3")
	links := map[string]string{
		"openjdk-bin-21":          java21,
		"openjdk-bin-21.0.11_p10": java21,
		"openjdk-bin-25":          java25,
		"openjdk-bin-25.0.3_p9":   java25,
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(jvm, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(jvm, "openjdk-bin-25", "bin", "java"), filepath.Join(bin, "java")); err != nil {
		t.Fatal(err)
	}

	catalogDir := filepath.Join(root, "catalog")
	appsDir := filepath.Join(catalogDir, "apps")
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{appsDir, servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeApp := func(file, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(appsDir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeApp("java.yml", fmt.Sprintf(`
name: java-%%i-%%v
display_name: "Java ${instance} ${version} ${current}"
versions:
  current_from: "%s/java"
variables:
  binary:
    - "%s/${instance}-jre-bin-${version}/bin/java"
    - "%s/${instance}-jdk-bin-${version}/bin/java"
    - "%s/${instance}-bin-${version}/bin/java"
    - "%s/${instance}-${version}/bin/java"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "-version"], timeout: 10s }
`, bin, jvm, jvm, jvm, jvm))
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, catalogDir, servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	tests := []struct {
		name        string
		binary      string
		displayName string
	}{
		{
			name:        "java",
			binary:      filepath.Join(bin, "java"),
			displayName: "Java",
		},
		{
			name:        "java-openjdk-21.0.11_p10",
			binary:      filepath.Join(jvm, "openjdk-bin-21", "bin", "java"),
			displayName: "Java openjdk 21.0.11_p10",
		},
		{
			name:        "java-openjdk-25.0.3_p9",
			binary:      filepath.Join(jvm, "openjdk-bin-25", "bin", "java"),
			displayName: "Java openjdk 25.0.3_p9 current",
		},
	}
	if _, ok := cfg.Apps["java-openjdk-21"]; ok {
		t.Fatalf("short Java version should be deduplicated")
	}
	if _, ok := cfg.Apps["java-openjdk-25"]; ok {
		t.Fatalf("short Java version should be deduplicated")
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, ok := cfg.Apps[tt.name]
			if !ok {
				t.Fatalf("app %q was not materialized", tt.name)
			}
			if got := DisplayName(doc.Body, tt.name); got != tt.displayName {
				t.Fatalf("%s display_name = %q, want %q", tt.name, got, tt.displayName)
			}
			if got := DocumentBinary(doc.Body); got != tt.binary {
				t.Fatalf("%s binary = %q, want %q", tt.name, got, tt.binary)
			}
			resolved, errs := cfg.ResolveCatalog(CategoryApp, tt.name)
			if len(errs) > 0 {
				t.Fatalf("ResolveCatalog(%s): %v", tt.name, errs)
			}
			if got := cfgval.String(valueAt(t, resolved.Tree, "variables", "binary")); got != tt.binary {
				t.Fatalf("%s resolved binary = %q, want %q", tt.name, got, tt.binary)
			}
		})
	}
}

func TestCompositeVersionTemplateCurrentFromMaterializesActiveSlot(t *testing.T) {
	cfg, err := Load(writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/java.yml": `
name: java-%i-%v
display_name: "Java ${instance} ${version}"
versions:
  current_from: /usr/bin/java
preflight:
  binary: { type: binary, path: "${binary}" }
`,
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	doc, ok := cfg.Apps["java"]
	if !ok {
		t.Fatalf("current_from did not materialize active app: %v", cfg.AppNames)
	}
	if got := DisplayName(doc.Body, "java"); got != "Java" {
		t.Fatalf("java display_name = %q, want Java", got)
	}
	if got := DocumentBinary(doc.Body); got != "/usr/bin/java" {
		t.Fatalf("java binary = %q, want /usr/bin/java", got)
	}
	if _, ok := cfg.Apps["java--"]; ok {
		t.Fatalf("active app was materialized with dangling separators")
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
name: python%%n
display_name: "Python ${n}"
variables:
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
		"catalog/apps/php.yml": fmt.Sprintf(`
name: php%%v
display_name: "PHP ${version}"
versions:
  unversioned: false
variables:
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
		"catalog/apps/php.yml": fmt.Sprintf(`
name: php%%v
display_name: "PHP ${version}"
description: "PHP runtime ${version}"
versions:
  unversioned:
    display_name: "System PHP"
    description: "Default PHP interpreter"
variables:
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
name: python%%n
display_name: "Python ${n}"
variables:
  binary: "%s/python${n}"
`, bin),
		"catalog/apps/python3.yml": fmt.Sprintf(`
name: python3
display_name: "Python Three"
variables:
  binary: "%s/python3"
`, bin),
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := strings.Join(cfg.CatalogNamesInCategory(CategoryApp), ","); got != "python3" {
		t.Fatalf("app names = %s, want python3", got)
	}
	if got := DisplayName(cfg.Apps["python3"].Body, "python3"); got != "Python Three" {
		t.Fatalf("python3 display_name = %q, want explicit canonical app", got)
	}
}

func TestInstanceTemplateMaterialization(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
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
	write(filepath.Join(catalogDir, "services"), "openvpn-base.yml", `
name: openvpn
display_name: OpenVPN
service: openvpn
variables: { port: 1194 }
checks:
  port: { type: openvpn, port: "${port}" }
`)
	write(filepath.Join(catalogDir, "apps"), "openvpn.yml", `
name: openvpn
display_name: OpenVPN
variables:
  binary: /usr/bin/openvpn
preflight:
  binary: { type: binary, path: "${binary}" }
`)
	tmpl := `
name: openvpn-%i
uses: openvpn
display_name: "OpenVPN ${instance}"
service: "openvpn.${instance}"
apps: [openvpn]
variables:
  config: "/etc/openvpn/${instance}.conf"
`
	write(filepath.Join(catalogDir, "services"), "openvpn.yml", tmpl)
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: openrc }
paths: { catalog: [ %s ], services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, catalogDir, servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global, WithServiceUnits("openrc", []string{"openvpn.tun1", "openvpn.client-a"}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.CatalogServices["openvpn-%i"]; ok {
		t.Fatal("template openvpn-%i should not be registered")
	}
	for _, inst := range []string{"client-a", "tun1"} {
		name := "openvpn-" + inst
		doc, ok := cfg.CatalogServices[name]
		if !ok {
			t.Fatalf("expected materialized service %q", name)
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

// TestVersionTemplateMaterialization exercises a `name: foo-%v` service template:
// it must produce one service per installed app version, inherit a `uses` base,
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

	catalogDir := filepath.Join(root, "catalog")
	catalogServicesDir := filepath.Join(catalogDir, "services")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
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
	write(catalogServicesDir, "php-fpm-base.yml", `
name: php-fpm
display_name: "PHP-FPM"
service: php-fpm
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
	write(filepath.Join(catalogDir, "apps"), "php-fpm.yml", fmt.Sprintf(`
name: php-fpm-%%v
display_name: "PHP-FPM ${version}"
variables:
  binary: "%s/php${version}/bin/php-fpm"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "-v"] }
`, binRoot))
	// Version template inheriting the base; installed versions come from the app.
	write(catalogServicesDir, "php-fpm-template.yml", `
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
  services: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, catalogDir, servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Template must be gone; one concrete service per installed version present.
	if _, ok := cfg.CatalogServices["php-fpm-%v"]; ok {
		t.Errorf("template php-fpm-%%v should not be registered")
	}
	for _, v := range []string{"7.4", "8.3"} {
		name := "php-fpm-" + v
		doc, ok := cfg.CatalogServices[name]
		if !ok {
			t.Fatalf("expected materialized service %q", name)
		}
		// display_name has the version baked in (no literal ${version}).
		if got := DisplayName(doc.Body, name); got != "PHP-FPM "+v {
			t.Errorf("%s display_name = %q, want %q", name, got, "PHP-FPM "+v)
		}
		// Inherited the base rule; the versioned binary belongs to the linked app.
		wantBin := fmt.Sprintf("%s/php%s/bin/php-fpm", binRoot, v)
		if got := DocumentBinary(doc.Body); got != "/usr/sbin/php-fpm" {
			t.Errorf("%s binary = %q, want inherited /usr/sbin/php-fpm", name, got)
		}
		appDoc, ok := cfg.Apps[name]
		if !ok {
			t.Fatalf("expected materialized app %q", name)
		}
		if got := DocumentBinary(appDoc.Body); got != wantBin {
			t.Errorf("%s app binary = %q, want %q", name, got, wantBin)
		}
		if _, ok := nested(t, doc.Body, "rules")["block-bad-config"]; !ok {
			t.Errorf("%s did not inherit base rule", name)
		}
	}

	// A service using a materialized version resolves end to end, including the
	// inherited rule message expanding through the baked display_name.
	write(servicesDir, "site.yml", `
name: site
uses: php-fpm-8.3
service: php-fpm
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
	detectedHostname = "node1"
	defer func() { detectedHostname = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/mon.yml": `
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
	if got := ServiceUnit(resolved.Tree, "mon"); got != "ceph-mon@node1" {
		t.Errorf("service unit = %q, want ceph-mon@node1", got)
	}
}

func TestUserHostnameVariableOverridesBuiltin(t *testing.T) {
	old := detectedHostname
	detectedHostname = "node1"
	defer func() { detectedHostname = old }()

	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/mon.yml": `
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
	// Catalog files take their kind from the subdirectory (services/ → service,
	// apps/ → app), so the template and its app must live in the right dirs.
	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	write := func(dir, file, content string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(catalogDir, "apps"), "ceph-osd.yml", `
name: ceph-osd
display_name: "Ceph OSD"
variables:
  binary: /usr/bin/ceph-osd
preflight:
  binary: { type: binary, path: "${binary}" }
`)
	write(filepath.Join(catalogDir, "services"), "ceph-osd-%n.yml", `
name: ceph-osd%n
display_name: "Ceph OSD ${n}"
service: "ceph-osd@${n}"
apps: [ceph-osd]
variables: { user: ceph }
checks: { service: { type: service, expect: active } }
`)
	// One enabled service per OSD that uses the materialized service.
	write(servicesDir, "osd0.yml", "name: osd0\nuses: ceph-osd0\n")

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: systemd }
paths:
  catalog: [ %s ]
  services: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, catalogDir, servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global, WithServiceUnits("systemd", []string{
		"ceph-osd@0.service",
		"ceph-osd@1.service",
		"ceph-osd@3.service",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// Template gone; one concrete service per active OSD unit; absent id 2 not present.
	if _, ok := cfg.CatalogServices["ceph-osd%n"]; ok {
		t.Errorf("template ceph-osd%%n should not be registered")
	}
	if _, ok := cfg.CatalogServices["ceph-osd2"]; ok {
		t.Errorf("ceph-osd2 must not exist (no active ceph-osd@2.service)")
	}
	for _, id := range []string{"0", "1", "3"} {
		name := "ceph-osd" + id
		doc, ok := cfg.CatalogServices[name]
		if !ok {
			t.Fatalf("expected materialized service %q", name)
		}
		// ${n} baked into the unit name at materialization.
		if got := ServiceUnit(doc.Body, name); got != "ceph-osd@"+id {
			t.Errorf("%s service unit = %q, want ceph-osd@%s", name, got, id)
		}
	}
	// The app link survives materialization: a service using ceph-osd0 resolves
	// cleanly (the generic ceph-osd app's preflight binary check is wired in).
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
	catalogDir := filepath.Join(root, "catalog")
	catalogServicesDir := filepath.Join(catalogDir, "services")
	servicesDir := filepath.Join(root, "services")
	for _, d := range []string{catalogServicesDir, servicesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	appsDir := filepath.Join(catalogDir, "apps")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appsDir, "ceph-osd.yml"), []byte(`
name: ceph-osd
variables:
  binary: /usr/bin/ceph-osd
preflight:
  binary: { type: binary, path: "${binary}" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogServicesDir, "ceph-osd-%n.yml"), []byte(`
name: ceph-osd%n
service: "ceph-osd@${n}"
apps: [ceph-osd]
variables: { user: ceph }
checks: { service: { type: service, expect: active } }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: systemd }
paths:
  catalog: [ %s ]
  services: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, catalogDir, servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global, WithServiceUnits("systemd", nil))
	if err != nil {
		t.Fatalf("Load() with no OSDs must not error, got %v", err)
	}
	for name := range cfg.CatalogServices {
		if strings.HasPrefix(name, "ceph-osd") {
			t.Errorf("no ceph-osd services expected with zero discovery matches, got %q", name)
		}
	}
}

func TestExpandAnalyzeResolvesUseSilenceRules(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/patterns/common.yml": `
name: common
rules:
  - { id: dep,  match: "(?i)deprecated", severity: warning }
  - { id: note, match: "(?i)note",       severity: warning }
`,
		"catalog/services/svc.yml": `
name: svc
variables:
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
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
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
			"catalog/patterns/common.yml": "name: common\nrules:\n  - { id: dep, match: x, severity: warning }\n",
			"catalog/services/svc.yml":    "name: svc\nbinary: /bin/true\nchecks:\n  config:\n    type: command\n    command: [\"${binary}\"]\n    analyze:\n" + analyze,
			"services/svc-main.yml":       "name: svc-main\nuses: svc\n",
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
		"catalog/services/svc.yml": `
name: svc
pidfile: /run/svc.pid
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("svc-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if got := cfgval.String(resolved.Tree["pidfile"]); got != "/run/svc.pid" {
		t.Fatalf("pidfile = %q, want /run/svc.pid", got)
	}
	if _, present := resolved.Tree["processes"]; present {
		t.Fatalf("pidfile must not create public processes entry: %v", resolved.Tree["processes"])
	}
	// Gated health check.
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
		"catalog/services/svc.yml": `
name: svc
pidfile:
  - /run/svc-main.pid
  - /run/svc-legacy.pid
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
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
	if got := cfgval.StringList(resolved.Tree["pidfile"]); !slices.Equal(got, want) {
		t.Fatalf("pidfile paths = %v, want %v", got, want)
	}
	checks := resolved.Tree["checks"].(map[string]any)
	chk := checks["pidfile"].(map[string]any)
	if got := cfgval.StringList(chk["path"]); !slices.Equal(got, want) {
		t.Fatalf("check pidfile paths = %v, want %v", got, want)
	}
}

func TestExpandPidfileOptionalMapDesugars(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc
pidfile:
  path: /run/svc.pid
  optional: true
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("svc-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if got := cfgval.String(resolved.Tree["pidfile"]); got != "/run/svc.pid" {
		t.Fatalf("pidfile = %q, want /run/svc.pid", got)
	}
	chk := nested(t, resolved.Tree, "checks", "pidfile")
	if optional, _ := chk["optional"].(bool); !optional {
		t.Fatalf("pidfile check optional = %v, want true", chk["optional"])
	}
}

func TestExpandPidfilesDesugarsByRole(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc
pidfiles:
  main:
    - /run/svc-main.pid
    - /run/svc.pid
  helper: /run/svc-helper.pid
processes:
  main:
    exe: /usr/sbin/svc
    user: svc
  helper:
    exe: /usr/sbin/svc-helper
    user: svc
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("svc-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	pidfiles := resolved.Tree["pidfiles"].(map[string]any)
	if got, want := cfgval.StringList(pidfiles["main"]), []string{"/run/svc-main.pid", "/run/svc.pid"}; !slices.Equal(got, want) {
		t.Fatalf("pidfiles.main = %v, want %v", got, want)
	}
	if got := cfgval.String(pidfiles["helper"]); got != "/run/svc-helper.pid" {
		t.Fatalf("pidfiles.helper = %q, want /run/svc-helper.pid", got)
	}
	checks := resolved.Tree["checks"].(map[string]any)
	main := checks["pidfile-main"].(map[string]any)
	if got := cfgval.StringList(main["path"]); !slices.Equal(got, []string{"/run/svc-main.pid", "/run/svc.pid"}) {
		t.Fatalf("check pidfile-main path = %v", got)
	}
	helper := checks["pidfile-helper"].(map[string]any)
	if got := cfgval.String(helper["path"]); got != "/run/svc-helper.pid" {
		t.Fatalf("check pidfile-helper path = %v", got)
	}
}

func TestValidatePidfilesRequireMatchingProcessIdentity(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc
pidfile: /run/legacy.pid
pidfiles:
  missing: /run/missing.pid
  relative: run/relative.pid
  no-exe: /run/no-exe.pid
  no-user: /run/no-user.pid
processes:
  no-exe:
    user: svc
    cmd: svc --no-exe
  no-user:
    exe: /usr/sbin/no-user
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	issues := Validate(cfg)
	for _, want := range []string{
		"pidfile and pidfiles are mutually exclusive",
		"pidfiles.missing requires matching processes.missing",
		"pidfiles.no-exe requires processes.no-exe.exe",
		"pidfiles.no-user requires processes.no-user.user",
		`pidfiles.relative path "run/relative.pid" must be absolute`,
	} {
		if !hasIssue(issues, want) {
			t.Errorf("missing issue containing %q in %v", want, issues)
		}
	}
}

func TestExpandSocketDesugars(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc
socket:
  path:
    - /run/svc-main.sock
    - /run/svc-legacy.sock
  optional: true
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("svc-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if _, present := resolved.Tree["socket"]; present {
		t.Errorf("top-level socket key must be consumed")
	}
	checks := resolved.Tree["checks"].(map[string]any)
	chk := checks["socket"].(map[string]any)
	want := []string{"/run/svc-main.sock", "/run/svc-legacy.sock"}
	if chk["type"] != "socket" || !slices.Equal(cfgval.StringList(chk["path"]), want) {
		t.Fatalf("socket check = %v, want candidate list %v", chk, want)
	}
	if optional, _ := chk["optional"].(bool); !optional {
		t.Fatalf("socket check optional = %v, want true", chk["optional"])
	}
	req, _ := chk["requires"].([]any)
	if len(req) != 1 || req[0] != "service" {
		t.Fatalf("socket check requires = %v, want [service]", chk["requires"])
	}
}

func TestExpandSocketUsesVariable(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc
variables:
  socket: /run/svc.sock
socket: "${socket}"
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("svc-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	chk := nested(t, resolved.Tree, "checks", "socket")
	if got := cfgval.String(chk["path"]); got != "/run/svc.sock" {
		t.Fatalf("socket check path = %q, want /run/svc.sock", got)
	}
}

func TestExpandLockfileDesugars(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc
lockfile:
  path:
    - /run/lock/svc-main.lock
    - /run/lock/svc-legacy.lock
  optional: true
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("svc-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	if _, present := resolved.Tree["lockfile"]; present {
		t.Errorf("top-level lockfile key must be consumed")
	}
	checks := resolved.Tree["checks"].(map[string]any)
	chk := checks["lockfile"].(map[string]any)
	want := []string{"/run/lock/svc-main.lock", "/run/lock/svc-legacy.lock"}
	if chk["type"] != "lockfile" || !slices.Equal(cfgval.StringList(chk["path"]), want) {
		t.Fatalf("lockfile check = %v, want candidate list %v", chk, want)
	}
	if optional, _ := chk["optional"].(bool); !optional {
		t.Fatalf("lockfile check optional = %v, want true", chk["optional"])
	}
	req, _ := chk["requires"].([]any)
	if len(req) != 1 || req[0] != "service" {
		t.Fatalf("lockfile check requires = %v, want [service]", chk["requires"])
	}
}

func TestExpandLockfileUsesVariable(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc
variables:
  lockfile: /run/lock/svc.lock
lockfile: "${lockfile}"
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, errs := cfg.Resolve("svc-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve() errors = %v", errs)
	}
	chk := nested(t, resolved.Tree, "checks", "lockfile")
	if got := cfgval.String(chk["path"]); got != "/run/lock/svc.lock" {
		t.Fatalf("lockfile check path = %q, want /run/lock/svc.lock", got)
	}
}

func TestExpandLockfileRejectsRelativeCandidate(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/svc.yml": `
name: svc
lockfile: run/lock/svc.lock
checks:
  service: { type: service, expect: active }
`,
		"services/svc-main.yml": "name: svc-main\nuses: svc\n",
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	_, errs := cfg.Resolve("svc-main")
	if !hasSub(errs, `lockfile path "run/lock/svc.lock" must be absolute`) {
		t.Fatalf("Resolve() errors = %v, want relative lockfile path error", errs)
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
name: s
service: { systemd: [docker] }
also_service: { systemd: [docker] }
`), "primary service unit")

	mustHave(t, validateService(t, `
name: s
service: { systemd: [docker] }
also_service: { foo: [x] }
`), "not one of systemd, openrc")

	mustHave(t, validateService(t, `
name: s
service: { systemd: [docker] }
also_service: { systemd: [docker.socket, 7] }
`), "also_service.systemd must be a non-empty list")
}

func TestStopInvariants(t *testing.T) {
	tree := map[string]any{
		"pidfile": "/run/svc.pid",
		"pidfiles": map[string]any{
			"helper": "/run/svc-helper.pid",
			"worker": []any{"/run/svc-worker.pid", "/run/svc-worker-legacy.pid"},
		},
		"stop_policy": map[string]any{
			"pidfile_absent":   true,
			"files_absent":     []any{"/run/svc/*.sock"},
			"clean_after_stop": true,
		},
	}
	pp, ff, cleanEnabled, _ := StopInvariants(tree)
	wantPidfiles := []string{"/run/svc.pid", "/run/svc-helper.pid", "/run/svc-worker.pid", "/run/svc-worker-legacy.pid"}
	if !slices.Equal(pp, wantPidfiles) {
		t.Fatalf("pidfile paths = %v, want %v", pp, wantPidfiles)
	}
	if len(ff) != 1 || ff[0] != "/run/svc/*.sock" || !cleanEnabled {
		t.Fatalf("files=%v cleanEnabled=%v", ff, cleanEnabled)
	}
	// pidfile_absent omitted -> no pidfile paths even if pidfile is declared.
	pp2, _, _, _ := StopInvariants(map[string]any{
		"pidfile":     tree["pidfile"],
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
  services: [ @ROOT@/services ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
  variables:
    cvar: /opt/data
    host: 10.0.0.9
`,
		"catalog/services/svc.yml": `
name: svc
checks:
  f: { type: file_exists, path: "${cvar}/file" }
  h: { type: command, command: ["echo", "${host}"] }
`,
		"services/a.yml": "name: a\nuses: svc\n",
		// b overrides the custom host with its own variable.
		"services/b.yml": "name: b\nuses: svc\nvariables: { host: 127.0.0.1 }\n",
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

func TestResolveWatchesExpandsCustomVars(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
engine: { backend: auto }
paths:
  catalog: [ @ROOT@/catalog ]
  services: [ @ROOT@/services ]
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
name: glibc
variables:
  binary: "/lib64/libc.so.6"
`,
		"services/web.yml": `
name: web
service: web
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
		"services/web.yml": `
name: web
service: web
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

// TestCatalogServiceOwnsDiscovery covers the v2 rule: a catalog service template that declares
// its own token-bearing `versions.from` materializes from that path directly,
// without needing a linked discovery app.
func TestCatalogServiceOwnsDiscovery(t *testing.T) {
	root := t.TempDir()
	confd := filepath.Join(root, "conf")
	if err := os.MkdirAll(confd, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"myd-tun1.conf", "myd-tun2.conf"} {
		if err := os.WriteFile(filepath.Join(confd, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
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
	write(filepath.Join(catalogDir, "services"), "myd-%i.yml", fmt.Sprintf(`
name: myd-%%i
display_name: "Myd ${instance}"
service: "myd.${instance}"
versions:
  from: "%s/myd-${instance}.conf"
checks:
  service: { type: service, expect: active }
`, confd))
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, catalogDir, servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.CatalogServices["myd-%i"]; ok {
		t.Fatal("template myd-%i should not be registered")
	}
	for _, inst := range []string{"tun1", "tun2"} {
		name := "myd-" + inst
		doc, ok := cfg.CatalogServices[name]
		if !ok {
			t.Fatalf("expected materialized service %q from service-owned discovery", name)
		}
		if got := ServiceUnit(doc.Body, name); got != "myd."+inst {
			t.Fatalf("%s service unit = %q, want myd.%s", name, got, inst)
		}
	}
}

// TestMultiTokenSeparatorMaterialization covers a `name: tomcat-%v%s%i` template:
// version + optional separator + instance discovered together from service units.
// The no-instance case (tomcat-8.5) must materialize without a trailing separator.
func TestMultiTokenSeparatorMaterialization(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(catalogDir, "services")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tomcat-%v%s%i.yml"), []byte(fmt.Sprintf(`
name: tomcat-%%v%%s%%i
display_name: "Tomcat ${version} (${instance})"
service: "tomcat-${version}${sep}${instance}"
variables:
  config: "/etc/tomcat-${version}${sep}${instance}/server.xml"
checks:
  service: { type: service, expect: active }
`)), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: systemd }
paths: { catalog: [ %s ], services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, catalogDir, servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global, WithServiceUnits("systemd", []string{
		"tomcat-8.5-main.service",
		"tomcat-9-guacamole.service",
		"tomcat-8.5.service",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]string{
		"tomcat-8.5-main":    "tomcat-8.5-main",
		"tomcat-9-guacamole": "tomcat-9-guacamole",
		"tomcat-8.5":         "tomcat-8.5", // no instance -> no trailing separator
	}
	for name, unit := range want {
		doc, ok := cfg.CatalogServices[name]
		if !ok {
			t.Fatalf("expected materialized service %q", name)
		}
		if got := ServiceUnit(doc.Body, name); got != unit {
			t.Fatalf("%s service unit = %q, want %q", name, got, unit)
		}
	}
	if _, ok := cfg.CatalogServices["tomcat-8.5-"]; ok {
		t.Fatal("must not materialize a trailing-separator name tomcat-8.5-")
	}
}

// TestVariableFromFileExtraction covers a variable whose value is read from a
// config file: `directive: port` extracts the value after "port" (OpenVPN
// style), `pattern:` extracts a regex group, and `default:` applies when neither
// the file nor the key is present.
func TestVariableFromFileExtraction(t *testing.T) {
	root := t.TempDir()
	vpnConf := filepath.Join(root, "vpn.conf")
	if err := os.WriteFile(vpnConf, []byte("# comment\nproto udp\nport 1195\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tomcatConf := filepath.Join(root, "server.xml")
	if err := os.WriteFile(tomcatConf, []byte(`<Connector port="8081" protocol="HTTP/1.1"/>`), 0o644); err != nil {
		t.Fatal(err)
	}
	nebulaConf := filepath.Join(root, "nebula.yml")
	if err := os.WriteFile(nebulaConf, []byte(`static_host_map:
  "203.0.113.1": ["178.33.30.216:4243"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalogDir := filepath.Join(root, "catalog", "services")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(file, content string) {
		if err := os.WriteFile(filepath.Join(catalogDir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	enable := func(name, service string) {
		body := fmt.Sprintf("name: %s\nuses: %s\n", name, service)
		if err := os.WriteFile(filepath.Join(servicesDir, name+".yml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	enable("myvpn", "vpn")
	enable("mycat", "cat")
	enable("mynb", "nebula")
	enable("mydfl", "dfl")
	write("vpn.yml", fmt.Sprintf(`
name: vpn
service: vpn
variables:
  config: "%s"
  port: { from_file: "${config}", directive: port, default: 1194 }
checks:
  tcp: { type: tcp, host: 127.0.0.1, port: "${port}", timeout: 2s }
`, vpnConf))
	write("cat.yml", fmt.Sprintf(`
name: cat
service: cat
variables:
  config: "%s"
  port: { from_file: "${config}", pattern: '<Connector[^>]*?\bport="(\d+)"', default: 8080 }
checks:
  tcp: { type: tcp, host: 127.0.0.1, port: "${port}", timeout: 2s }
`, tomcatConf))
	write("nebula.yml", fmt.Sprintf(`
name: nebula
service: nebula
variables:
  config: "%s"
  host:
    from_file: "${config}"
    pattern: '(?m)^\s*static_host_map:\s*\n\s*(?:"[^"]+"|[^:\n]+)\s*:\s*\[\s*"\[?([^"\]]+)\]?:(?:\d+)"'
    default: 127.0.0.1
  port:
    from_file: "${config}"
    pattern: '(?m)^\s*static_host_map:\s*\n\s*(?:"[^"]+"|[^:\n]+)\s*:\s*\[\s*"[^"]+:(\d+)"'
    default: 4242
checks:
  tcp: { type: tcp, host: "${host}", port: "${port}", timeout: 2s }
`, nebulaConf))
	write("dfl.yml", `
name: dfl
service: dfl
variables:
  config: "/nonexistent/path.conf"
  port: { from_file: "${config}", directive: port, default: 1194 }
checks:
  tcp: { type: tcp, host: 127.0.0.1, port: "${port}", timeout: 2s }
`)
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, filepath.Join(root, "catalog"), servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, tc := range []struct{ name, want string }{
		{"myvpn", "1195"},
		{"mycat", "8081"},
		{"mynb", "4243"},
		{"mydfl", "1194"},
	} {
		resolved, errs := cfg.Resolve(tc.name)
		if len(errs) != 0 {
			t.Fatalf("Resolve(%s) errors = %v", tc.name, errs)
		}
		if got := cfgval.String(nested(t, resolved.Tree, "checks", "tcp")["port"]); got != tc.want {
			t.Errorf("%s: port = %q, want %q", tc.name, got, tc.want)
		}
		if tc.name == "mynb" {
			if got := cfgval.String(nested(t, resolved.Tree, "checks", "tcp")["host"]); got != "178.33.30.216" {
				t.Errorf("%s: host = %q, want 178.33.30.216", tc.name, got)
			}
		}
	}
}

func TestNUTDriverServiceResolvesUPSConfigDriver(t *testing.T) {
	root := t.TempDir()
	upsConf := filepath.Join(root, "ups.conf")
	if err := os.WriteFile(upsConf, []byte(`
[sai1]
  driver = usbhid-ups
  port = auto

[rack.snmp]
  driver = snmp-ups
  port = 192.0.2.10
`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalogDir := filepath.Join(root, "catalog", "services")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	nutBody := fmt.Sprintf(`
display_name: NUT UPS Drivers
category: hardware
service:
  systemd:
    - nut-driver.target
  openrc:
    - upsdrv
variables:
  config: %s
  instance:
    from_file: ${config}
    pattern: (?m)^\s*\[([A-Za-z0-9_.-]+)\]\s*$
    default: ""
  driver:
    from_file: ${config}
    pattern: (?m)^\s*driver\s*=\s*([A-Za-z0-9_.-]+)\s*$
    default: usbhid-ups
pidfile: /run/nut/${driver}-${instance}.pid
processes:
  main:
    cmd: /nut/${driver}(?:\s|$)
checks:
  process:
    type: process
    exe_any:
      - /usr/lib64/nut/${driver}
      - /lib64/nut/${driver}
`, upsConf)
	write(filepath.Join(catalogDir, "upsdrv.yml"), "name: upsdrv\n"+nutBody)
	write(filepath.Join(catalogDir, "upsdrv-instance.yml"), fmt.Sprintf(`name: upsdrv.%%i
display_name: NUT UPS Driver ${instance}
category: hardware
service:
  systemd:
    - nut-driver@${instance}
  openrc:
    - upsdrv.${instance}
variables:
  config: %s
  driver:
    from_file: ${config}
    pattern: (?ms)^\s*\[${instance}\]\s*$.*?^\s*driver\s*=\s*([A-Za-z0-9_.-]+)\s*$
    default: usbhid-ups
pidfile: /run/nut/${driver}-${instance}.pid
checks:
  process:
    type: process
    exe_any:
      - /usr/lib64/nut/${driver}
      - /lib64/nut/${driver}
`, upsConf))
	write(filepath.Join(servicesDir, "upsdrv.yml"), "name: upsdrv-main\nuses: upsdrv\n")
	write(filepath.Join(servicesDir, "rack.yml"), "name: rack\nuses: upsdrv.rack.snmp\n")
	global := filepath.Join(root, "sermo.yml")
	write(global, fmt.Sprintf(`
engine: { backend: systemd }
paths: { catalog: [ %s ], services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, filepath.Join(root, "catalog"), servicesDir))

	cfg, err := Load(global, WithServiceUnits("systemd", []string{"nut-driver@rack.snmp.service"}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.CatalogServices["upsdrv.rack.snmp"]; !ok {
		t.Fatal("expected materialized upsdrv.rack.snmp catalog service")
	}

	base, errs := cfg.Resolve("upsdrv-main")
	if len(errs) != 0 {
		t.Fatalf("Resolve(upsdrv-main) errors = %v", errs)
	}
	if got := cfgval.String(base.Tree["pidfile"]); got != "/run/nut/usbhid-ups-sai1.pid" {
		t.Fatalf("base pidfile = %q, want /run/nut/usbhid-ups-sai1.pid", got)
	}
	baseExes := cfgval.StringList(nested(t, base.Tree, "checks", "process")["exe_any"])
	if !slices.Contains(baseExes, "/usr/lib64/nut/usbhid-ups") {
		t.Fatalf("base process exe_any = %v, want usbhid-ups path", baseExes)
	}

	inst, errs := cfg.Resolve("rack")
	if len(errs) != 0 {
		t.Fatalf("Resolve(rack) errors = %v", errs)
	}
	if got := ServiceUnit(inst.Tree, "rack"); got != "nut-driver@rack.snmp" {
		t.Fatalf("instance service = %q, want nut-driver@rack.snmp", got)
	}
	if got := cfgval.String(inst.Tree["pidfile"]); got != "/run/nut/snmp-ups-rack.snmp.pid" {
		t.Fatalf("instance pidfile = %q, want /run/nut/snmp-ups-rack.snmp.pid", got)
	}
	instExes := cfgval.StringList(nested(t, inst.Tree, "checks", "process")["exe_any"])
	if !slices.Contains(instExes, "/usr/lib64/nut/snmp-ups") {
		t.Fatalf("instance process exe_any = %v, want snmp-ups path", instExes)
	}
}

// TestEnableIfPrunesByConfdFile covers the enable_if directive: a process branch
// is kept only when a key in a distro conf file satisfies the predicate (e.g.
// winbindd present in /etc/conf.d/samba's daemon_list). An absent file or
// unmatched key prunes the branch (fail-safe).
func TestEnableIfPrunesByConfdFile(t *testing.T) {
	root := t.TempDir()
	withWinbind := filepath.Join(root, "samba.on")
	if err := os.WriteFile(withWinbind, []byte(`daemon_list="smbd nmbd winbindd"`), 0o644); err != nil {
		t.Fatal(err)
	}
	withoutWinbind := filepath.Join(root, "samba.off")
	if err := os.WriteFile(withoutWinbind, []byte(`daemon_list="smbd nmbd"`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalogDir := filepath.Join(root, "catalog", "services")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogService := func(name, confFile string) {
		body := fmt.Sprintf(`
name: %s
service: %s
processes:
  smbd: { exe: /usr/sbin/smbd }
  winbindd:
    exe: /usr/sbin/winbindd
    enable_if: { file: "%s", key: daemon_list, contains: winbindd }
checks:
  service: { type: service, expect: active }
  winbindd:
    type: process
    exe: /usr/sbin/winbindd
    state: running
    enable_if: { file: "%s", key: daemon_list, contains: winbindd }
`, name, name, confFile, confFile)
		if err := os.WriteFile(filepath.Join(catalogDir, name+".yml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		svc := fmt.Sprintf("name: my%s\nuses: %s\n", name, name)
		if err := os.WriteFile(filepath.Join(servicesDir, "my"+name+".yml"), []byte(svc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	catalogService("sambaon", withWinbind)
	catalogService("sambaoff", withoutWinbind)
	catalogService("sambanone", filepath.Join(root, "missing"))
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, filepath.Join(root, "catalog"), servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, tc := range []struct {
		svc      string
		winbindd bool
	}{
		{"mysambaon", true},
		{"mysambaoff", false},
		{"mysambanone", false},
	} {
		resolved, errs := cfg.Resolve(tc.svc)
		if len(errs) != 0 {
			t.Fatalf("Resolve(%s) errors = %v", tc.svc, errs)
		}
		procs := nested(t, resolved.Tree, "processes")
		checks := nested(t, resolved.Tree, "checks")
		win, ok := procs["winbindd"]
		if ok != tc.winbindd {
			t.Errorf("%s: winbindd present = %v, want %v", tc.svc, ok, tc.winbindd)
		}
		if _, ok := checks["winbindd"]; ok != tc.winbindd {
			t.Errorf("%s: winbindd check present = %v, want %v", tc.svc, ok, tc.winbindd)
		}
		if _, ok := procs["smbd"]; !ok {
			t.Errorf("%s: smbd must always be present", tc.svc)
		}
		if tc.winbindd {
			if _, has := win.(map[string]any)["enable_if"]; has {
				t.Errorf("%s: enable_if must be stripped from a surviving branch", tc.svc)
			}
		}
	}
}

// TestMultiTokenDiscoveryRequireGate covers `versions.require`: an instance
// discovered from config (php-fpm pools, tomcat envs) is materialized only when
// its required binary also exists, so a stray config directory whose runtime is
// not installed does not produce a service with a dangling app link.
func TestMultiTokenDiscoveryRequireGate(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	bin := filepath.Join(root, "bin")
	for _, d := range []string{"app8.4", "app8.4_pool", "app5.6"} {
		if err := os.MkdirAll(filepath.Join(etc, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Binary present only for 8.4 (so 8.4 and 8.4_pool keep it; 5.6 is gated out).
	if err := os.WriteFile(filepath.Join(bin, "app8.4"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	catalogDir := filepath.Join(root, "catalog", "services")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "app%v%s%i.yml"), []byte(fmt.Sprintf(`
name: app%%v%%s%%i
service: "app${version}${sep}${instance}"
versions:
  from: "%s/app${version}${sep}${instance}"
  require: "%s/app${version}"
checks:
  service: { type: service, expect: active }
`, etc, bin)), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, filepath.Join(root, "catalog"), servicesDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, name := range []string{"app8.4", "app8.4_pool"} {
		if _, ok := cfg.CatalogServices[name]; !ok {
			t.Errorf("expected %q to materialize (binary present)", name)
		}
	}
	if _, ok := cfg.CatalogServices["app5.6"]; ok {
		t.Error("app5.6 must be gated out: its required binary is absent")
	}
}
