package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sermo/internal/cfgval"
)

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

func TestLibraryBinaryVariableIsPlainVariable(t *testing.T) {
	cfg := loadCatalog(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/libs/libdemo.yml": `
name: libdemo
variables:
  binary: /usr/lib64/libdemo.so.1
preflight:
  version: { type: command, command: ["/usr/bin/strings", "${binary}"], timeout: 10s, optional: true }
`,
	})
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

	cfg, err := loadConfig(t, global)
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

func TestBuiltinHostServiceAndRuntimeVars(t *testing.T) {
	old := detectedHost
	detectedHost = "myhost"
	defer func() { detectedHost = old }()

	resolved := resolveInstance(t, map[string]string{
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
      message: "${service} on ${host}: ${event}/${action} at ${date}; ${rule.duration}/${rule.window}; ${check.name}/${check.type}/${check.metric}/${check.scope}/${check.op}/${check.threshold}/${check.value}"
`,
	}, "web")
	// ${host} falls back to the hostname (no user-defined host variable).
	if got := cfgval.String(nested(t, resolved.Tree, "checks", "ping")["host"]); got != "myhost" {
		t.Errorf("ping host = %q, want myhost", got)
	}
	// ${service} → the backend unit name; ${host} resolved; runtime vars deferred.
	msg := cfgval.String(nested(t, resolved.Tree, "rules", "alert-down", "then")["message"])
	if !strings.Contains(msg, "nginx on myhost") {
		t.Errorf("message = %q, want service/host substituted", msg)
	}
	for _, lit := range []string{
		"${event}",
		"${action}",
		"${date}",
		"${rule.duration}",
		"${rule.window}",
		"${check.name}",
		"${check.type}",
		"${check.metric}",
		"${check.scope}",
		"${check.op}",
		"${check.threshold}",
		"${check.value}",
	} {
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
	cfg, err := loadConfig(t, global)
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
	cfg, err := loadConfig(t, global)
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
	assertResolvedCheckField(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/web.yml": `
name: web
service: web
variables:
  host: 127.0.0.1
checks:
  ping: { type: tcp, host: "${host}", port: "80" }
`,
	}, "web", "ping", "host", "127.0.0.1")
}

func TestBuiltinPortVariable(t *testing.T) {
	// A top-level `port:` field feeds the built-in ${port}.
	assertResolvedCheckField(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/db.yml": `
name: db
service: db
port: 6379
checks:
  ping: { type: tcp, host: "127.0.0.1", port: "${port}" }
`,
	}, "db", "ping", "port", "6379")
}

func TestUserPortVariableOverridesBuiltin(t *testing.T) {
	// An explicit variables.port wins over the top-level `port:` field.
	assertResolvedCheckField(t, map[string]string{
		"sermo.yml": baseGlobal,
		"services/db.yml": `
name: db
service: db
port: 6379
variables: { port: 7000 }
checks:
  ping: { type: tcp, host: "127.0.0.1", port: "${port}" }
`,
	}, "db", "ping", "port", "7000")
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
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, errs := cfg.Resolve("db"); len(errs) == 0 {
		t.Fatal("a ${port} with no port defined must error")
	}
}

func TestGlobalCustomVariables(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
engine: { backend: auto }
paths:
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
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// custom var used in a service-level variable, then in a check.
	ra, errs := cfg.Resolve("a")
	if len(errs) != 0 {
		t.Fatalf("resolve a: %v", errs)
	}
	checkEntries := ra.Tree["checks"].(map[string]any)
	if got := checkEntries["f"].(map[string]any)["path"]; got != "/opt/data/file" {
		t.Fatalf("custom var not expanded: %v", got)
	}
	// custom host overrides the builtin host (custom > builtins).
	if got := checkEntries["h"].(map[string]any)["command"].([]any)[1]; got != "10.0.0.9" {
		t.Fatalf("custom host should override builtin, got %v", got)
	}
	// a service's own variable overrides the custom one (service > custom).
	rb, _ := cfg.Resolve("b")
	cmd := rb.Tree["checks"].(map[string]any)["h"].(map[string]any)["command"].([]any)
	if cmd[1] != "127.0.0.1" {
		t.Fatalf("service variable should override custom, got %v", cmd[1])
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
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: auto }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global)
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
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir))

	cfg, err := loadConfig(t, global, withServiceUnits("systemd", []string{"nut-driver@rack.snmp.service"}))
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
