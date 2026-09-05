package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		checkEntries := nested(t, resolved.Tree, "checks")
		win, ok := procs["winbindd"]
		if ok != tc.winbindd {
			t.Errorf("%s: winbindd present = %v, want %v", tc.svc, ok, tc.winbindd)
		}
		if _, ok := checkEntries["winbindd"]; ok != tc.winbindd {
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

// TestEnableIfReadsSpacedAssignment covers config formats such as exim.conf's,
// which pad the separator: `tls_on_connect_ports = 465`. Without it no gate can
// be expressed against an exim option at all. The unpadded forms are asserted
// alongside, because the padding skip must be a strict widening.
func TestEnableIfReadsSpacedAssignment(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "catalog", "services")
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{catalogDir, servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name       string
		configBody string
		wantGated  bool
	}{
		{name: "spaced", configBody: "tls_on_connect_ports = 465\n", wantGated: true},
		{name: "unpadded", configBody: "tls_on_connect_ports=465\n", wantGated: true},
		{name: "tabs", configBody: "tls_on_connect_ports\t=\t465\n", wantGated: true},
		{name: "spaced yaml", configBody: "tls_on_connect_ports : 465\n", wantGated: true},
		{name: "empty value", configBody: "tls_on_connect_ports =\n", wantGated: false},
		{name: "commented", configBody: "# tls_on_connect_ports = 465\n", wantGated: false},
		{name: "absent", configBody: "daemon_smtp_ports = 25\n", wantGated: false},
		{name: "prefix collision", configBody: "tls_on_connect_ports_extra = 465\n", wantGated: false},
		// snmpd.conf, sshd_config and named.conf-style files separate key and
		// value with whitespace alone.
		{name: "whitespace form", configBody: "tls_on_connect_ports 465\n", wantGated: true},
		{name: "whitespace quoted", configBody: "tls_on_connect_ports\t\"465\"\n", wantGated: true},
		{name: "whitespace prefix collision", configBody: "tls_on_connect_ports_extra 465\n", wantGated: false},
	}
	for _, tt := range tests {
		configPath := filepath.Join(root, strings.ReplaceAll(tt.name, " ", "-")+".conf")
		if err := os.WriteFile(configPath, []byte(tt.configBody), 0o644); err != nil {
			t.Fatal(err)
		}
		catalogName := strings.ReplaceAll(tt.name, " ", "-")
		body := fmt.Sprintf(`
name: %s
service: %s
checks:
  cert:
    enable_if: { file: %q, key: tls_on_connect_ports, matches: "[0-9]" }
    type: tcp
    host: 127.0.0.1
    port: 465
`, catalogName, catalogName, configPath)
		if err := os.WriteFile(filepath.Join(catalogDir, catalogName+".yml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		serviceName := "my-" + catalogName
		serviceBody := fmt.Sprintf("name: %s\nuses: %s\n", serviceName, catalogName)
		if err := os.WriteFile(filepath.Join(servicesDir, serviceName+".yml"), []byte(serviceBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: auto }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global, WithCatalogDirs(filepath.Dir(catalogDir)))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalogName := strings.ReplaceAll(tt.name, " ", "-")
			resolved, errs := cfg.Resolve("my-" + catalogName)
			if len(errs) != 0 {
				t.Fatalf("Resolve() errors = %v", errs)
			}
			checks, _ := resolved.Tree["checks"].(map[string]any)
			_, gotGated := checks["cert"]
			if gotGated != tt.wantGated {
				t.Errorf("cert check present = %v, want %v", gotGated, tt.wantGated)
			}
		})
	}
}

// TestEnableIfReadsYAMLBlockKey covers config formats such as cloudflared's,
// where the gate key is a YAML block mapping rather than an OpenRC assignment.
// The packaged cloudflared profile depends on it: `tunnel ingress validate`
// fails on a remotely-managed tunnel that declares no ingress, and a failed
// preflight blocks the restart, so the check must be pruned there and kept where
// ingress really is declared.
func TestEnableIfReadsYAMLBlockKey(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "catalog", "services")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		configBody  string
		wantIngress bool
	}{
		{name: "block key", configBody: "ingress:\n  - service: http_status:404\n", wantIngress: true},
		{name: "inline value", configBody: "ingress: []\n", wantIngress: true},
		{name: "token tunnel", configBody: "token: abc\nmetrics: 127.0.0.1:60123\n", wantIngress: false},
		{name: "commented key", configBody: "#ingress:\n", wantIngress: false},
	}
	for _, tt := range tests {
		configPath := filepath.Join(root, strings.ReplaceAll(tt.name, " ", "-")+".yml")
		if err := os.WriteFile(configPath, []byte(tt.configBody), 0o644); err != nil {
			t.Fatal(err)
		}
		catalogName := strings.ReplaceAll(tt.name, " ", "-")
		body := fmt.Sprintf(`
name: %s
service: %s
preflight:
  config:
    enable_if: { file: %q, key: ingress, matches: ".*" }
    type: command
    command: ["/bin/true"]
`, catalogName, catalogName, configPath)
		if err := os.WriteFile(filepath.Join(catalogDir, catalogName+".yml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		serviceName := "my-" + catalogName
		serviceBody := fmt.Sprintf("name: %s\nuses: %s\n", serviceName, catalogName)
		if err := os.WriteFile(filepath.Join(servicesDir, serviceName+".yml"), []byte(serviceBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: auto }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global, WithCatalogDirs(filepath.Dir(catalogDir)))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalogName := strings.ReplaceAll(tt.name, " ", "-")
			resolved, errs := cfg.Resolve("my-" + catalogName)
			if len(errs) != 0 {
				t.Fatalf("Resolve() errors = %v", errs)
			}
			preflight, _ := resolved.Tree["preflight"].(map[string]any)
			_, gotIngress := preflight["config"]
			if gotIngress != tt.wantIngress {
				t.Errorf("preflight config present = %v, want %v", gotIngress, tt.wantIngress)
			}
		})
	}
}

// TestEnableIfPrunesByBareConfigFlag covers config formats such as dnsmasq's,
// where an optional feature is enabled by a bare directive with no value.
func TestEnableIfPrunesByBareConfigFlag(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "catalog", "services")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		configBody string
		wantTFTP   bool
	}{
		{name: "bare flag", configBody: "enable-tftp\n", wantTFTP: true},
		{name: "commented flag", configBody: "#enable-tftp\n", wantTFTP: false},
		{name: "missing flag", configBody: "port=53\n", wantTFTP: false},
	}
	for _, tt := range tests {
		configPath := filepath.Join(root, tt.name+".conf")
		if err := os.WriteFile(configPath, []byte(tt.configBody), 0o644); err != nil {
			t.Fatal(err)
		}
		catalogName := strings.ReplaceAll(tt.name, " ", "-")
		body := fmt.Sprintf(`
name: %s
service: %s
checks:
  tftp:
    type: tftp
    host: 127.0.0.1
    enable_if: { file: %q, key: enable-tftp, equals: "" }
`, catalogName, catalogName, configPath)
		if err := os.WriteFile(filepath.Join(catalogDir, catalogName+".yml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		serviceName := "my-" + catalogName
		serviceBody := fmt.Sprintf("name: %s\nuses: %s\n", serviceName, catalogName)
		if err := os.WriteFile(filepath.Join(servicesDir, serviceName+".yml"), []byte(serviceBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: auto }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global, WithCatalogDirs(filepath.Dir(catalogDir)))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalogName := strings.ReplaceAll(tt.name, " ", "-")
			resolved, errs := cfg.Resolve("my-" + catalogName)
			if len(errs) != 0 {
				t.Fatalf("Resolve() errors = %v", errs)
			}
			checks, _ := resolved.Tree["checks"].(map[string]any)
			_, gotTFTP := checks["tftp"]
			if gotTFTP != tt.wantTFTP {
				t.Errorf("tftp check present = %v, want %v", gotTFTP, tt.wantTFTP)
			}
		})
	}
}

// TestEnableIfInitScopesProcessSelector covers `enable_if: {init: ...}`: an
// exact supervisor selector that only exists under one init backend (Gentoo's
// supervise-daemon runs salt-minion under OpenRC only). Leaving it in place on
// systemd made the restart identity guard block every operation on a unit whose
// only live processes are the shared Python interpreter.
func TestEnableIfInitScopesProcessSelector(t *testing.T) {
	const serviceBody = `
name: minion
service: minion
processes:
  main:
    cmd: (^|[[:space:]])/usr/bin/minion([[:space:]]|$)
    user: root
  supervisor:
    exe: /usr/bin/supervise-daemon
    user: root
    enable_if: { init: openrc }
`
	for _, tc := range []struct {
		name           string
		detectedInit   string
		backend        string
		envBackend     string
		wantSupervisor bool
	}{
		{name: "detected openrc", detectedInit: "openrc", backend: "auto", wantSupervisor: true},
		{name: "detected systemd", detectedInit: "systemd", backend: "auto", wantSupervisor: false},
		{name: "configured openrc overrides detection", detectedInit: "systemd", backend: "openrc", wantSupervisor: true},
		{name: "configured systemd overrides detection", detectedInit: "openrc", backend: "systemd", wantSupervisor: false},
		{name: "environment overrides config", detectedInit: "systemd", backend: "systemd", envBackend: "openrc", wantSupervisor: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldInit := detectedInit
			detectedInit = tc.detectedInit
			defer func() { detectedInit = oldInit }()
			t.Setenv(EnvBackendOverride, tc.envBackend)

			global := writeConfig(t, map[string]string{
				"sermo.yml":           strings.Replace(baseGlobal, "backend: auto", "backend: "+tc.backend, 1),
				"services/minion.yml": serviceBody,
			})
			cfg, err := loadConfig(t, global)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if issues := Validate(cfg); len(issues) != 0 {
				t.Fatalf("Validate() issues = %v", issues)
			}
			resolved, errs := cfg.Resolve("minion")
			if len(errs) != 0 {
				t.Fatalf("Resolve() errors = %v", errs)
			}
			procs := nested(t, resolved.Tree, "processes")
			sup, ok := procs["supervisor"]
			if ok != tc.wantSupervisor {
				t.Fatalf("supervisor present = %v, want %v", ok, tc.wantSupervisor)
			}
			if _, ok := procs["main"]; !ok {
				t.Error("main selector must always survive")
			}
			if tc.wantSupervisor {
				if _, has := sup.(map[string]any)["enable_if"]; has {
					t.Error("enable_if must be stripped from a surviving branch")
				}
			}
		})
	}
}

func TestEnableIfInitValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		spec    string
		wantSub string
	}{
		{name: "unknown backend", spec: `{ init: launchd }`, wantSub: "init must be systemd or openrc"},
		{name: "mixed with file", spec: `{ init: openrc, file: /etc/conf.d/x, key: k, equals: "" }`, wantSub: "init excludes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			global := writeConfig(t, map[string]string{
				"sermo.yml": baseGlobal,
				"services/minion.yml": fmt.Sprintf(`
name: minion
service: minion
processes:
  supervisor:
    exe: /usr/bin/supervise-daemon
    user: root
    enable_if: %s
`, tc.spec),
			})
			cfg, err := loadConfig(t, global)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !hasIssue(Validate(cfg), tc.wantSub) {
				t.Fatalf("Validate() missing issue containing %q; got %v", tc.wantSub, Validate(cfg))
			}
		})
	}
}
