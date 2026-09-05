package config

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"sermo/internal/cfgval"
)

func TestAppsLinkInjectsAppPreflight(t *testing.T) {
	resolved := resolveInstance(t, map[string]string{
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
	}, "tomcat-main")
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
	if !slices.Equal(resolved.Apps, []string{"java"}) {
		t.Errorf("resolved apps = %v, want [java]", resolved.Apps)
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
	cfg, err := loadConfig(t, global)
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
	resolved := resolveInstance(t, map[string]string{
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
	}, "cups")
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
	resolved := resolveInstance(t, map[string]string{
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
	}, "php-fpm")
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
	resolved := resolveInstance(t, map[string]string{
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
	}, "cups")
	configCmd, _ := nested(t, nested(t, resolved.Tree, "preflight"), "config")["command"].([]any)
	if got := fmt.Sprint(configCmd...); got != "/opt/cups/sbin/cupsd-t" {
		t.Fatalf("config command = %v, want service variable override", configCmd)
	}
}

func TestServiceVariablesOverrideSingleAppDefaults(t *testing.T) {
	resolved := resolveInstance(t, map[string]string{
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
	}, "php-fpm")
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
	cfg, err := loadConfig(t, global)
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
	assertValidateIssue(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/web-main.yml": `
name: web-main
apps: [no-such-app]
service: web
`,
	}, `apps references unknown app "no-such-app"`)
}

func TestValidateServiceAppsLinkInvalidShape(t *testing.T) {
	assertValidateIssue(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/web-main.yml": `
name: web-main
apps: [app, 7]
service: web
`,
	}, "apps must be a string or list of strings")
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
	cfg, err := loadConfig(t, global)
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
	cfg, err = loadConfig(t, global)
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
	cfg, err := loadConfig(t, global)
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
