package appinspect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/execx"
)

type testRunner map[string]execx.Result

func (r testRunner) Run(_ context.Context, name string, _ ...string) (execx.Result, error) {
	if res, ok := r[name]; ok {
		return res, nil
	}
	return execx.Result{ExitCode: 127}, fmt.Errorf("%s: not found", name)
}

type testUserRunner struct {
	testRunner
	users []string
	names []string
}

func (r *testUserRunner) RunUser(ctx context.Context, user string, name string, args ...string) (execx.Result, error) {
	r.users = append(r.users, user)
	r.names = append(r.names, name)
	return r.Run(ctx, name, args...)
}

func TestInspectUsesNamespacedAppPreflight(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "webd")
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := config.Resolved{Tree: map[string]any{
		"preflight": map[string]any{
			"web-binary":  map[string]any{"type": "binary", "path": binary},
			"web-version": map[string]any{"type": "command", "command": []any{binary, "--version"}},
		},
	}}

	report := Inspect(context.Background(), testRunner{
		binary: {Stdout: "Webd 1.2.3\n", ExitCode: 0},
	}, "web", resolved)
	if !report.Installed || !report.OK || report.Binary != binary || report.Status != "ok" {
		t.Fatalf("Inspect() = %+v, want installed ok report for namespaced binary", report)
	}
	if report.Version != "Webd 1.2.3" || report.VersionShort != "1.2.3" {
		t.Fatalf("version = %q short=%q, want Webd 1.2.3 / 1.2.3", report.Version, report.VersionShort)
	}
}

func TestInspectCommandUser(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "postgres")
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := config.Resolved{Tree: map[string]any{
		"preflight": map[string]any{
			"binary":  map[string]any{"type": "binary", "path": binary},
			"version": map[string]any{"type": "command", "user": "postgres", "command": []any{binary, "--version"}},
		},
	}}
	runner := &testUserRunner{testRunner: testRunner{binary: {Stdout: "postgres 17.5\n", ExitCode: 0}}}

	report := Inspect(context.Background(), runner, "postgres", resolved)
	if !report.OK || report.VersionShort != "17.5" {
		t.Fatalf("Inspect() = %+v, want ok postgres version", report)
	}
	if !slices.Equal(runner.users, []string{"postgres"}) || !slices.Equal(runner.names, []string{binary}) {
		t.Fatalf("RunUser calls users=%v names=%v", runner.users, runner.names)
	}
}

func TestListPolkitVersionFromPkexecIntegerOutput(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{binDir, filepath.Join(catalogDir, "apps"), filepath.Join(catalogDir, "services"), servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	polkitd := filepath.Join(binDir, "polkitd")
	pkexec := filepath.Join(binDir, "pkexec")
	for _, path := range []string{polkitd, pkexec} {
		if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "apps", "polkit.yml"), []byte(fmt.Sprintf(`kind: app
name: polkit
display_name: "Polkit"
category: system
variables:
  binary: %q
  pkexec: %q
preflight:
  binary: { type: binary, path: "${binary}" }
  pkexec: { type: binary, path: "${pkexec}" }
  version: { type: command, command: ["${pkexec}", "--version"], timeout: 10s }
`, polkitd, pkexec)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "services", "polkit.yml"), []byte(`kind: daemon
name: polkit
display_name: "Polkit"
category: system
service:
  systemd: [polkit]
apps: [polkit]
checks:
  service: { type: service, expect: active }
`), 0o644); err != nil {
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
	cfg, err := config.Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	runner := testRunner{pkexec: {Stdout: "pkexec version 126\n", ExitCode: 0}}

	apps := List(context.Background(), runner, cfg, config.CategoryApp, false)
	if len(apps) != 1 {
		t.Fatalf("app reports = %+v, want one installed polkit app", apps)
	}
	if apps[0].Version != "pkexec version 126" || apps[0].VersionShort != "126" {
		t.Fatalf("polkit app version = %q short=%q, want pkexec version 126 / 126", apps[0].Version, apps[0].VersionShort)
	}

	services := List(context.Background(), runner, cfg, config.CategoryService, false, WithOptionalVersion())
	if len(services) != 1 {
		t.Fatalf("service reports = %+v, want one installed polkit service", services)
	}
	if services[0].Version != "pkexec version 126" || services[0].VersionShort != "126" {
		t.Fatalf("polkit service version = %q short=%q, want pkexec version 126 / 126", services[0].Version, services[0].VersionShort)
	}
}

func TestListMarksTemplateCurrentByBaseShortVersion(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	jvmDir := filepath.Join(root, "jvm")
	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{binDir, jvmDir, filepath.Join(catalogDir, "apps"), servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	currentJava := filepath.Join(binDir, "java")
	java21 := filepath.Join(jvmDir, "openjdk-bin-21.0.11_p10", "bin", "java")
	java25 := filepath.Join(jvmDir, "openjdk-bin-25.0.3_p9", "bin", "java")
	for _, path := range []string{currentJava, java21, java25} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "apps", "java.yml"), []byte(fmt.Sprintf(`kind: app
name: java-%%i-%%v
display_name: "Java ${instance} ${version} ${current}"
versions:
  from: "%s/${instance}-bin-${version}/bin/java"
  current_from: "%s"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "-version"], timeout: 10s }
`, jvmDir, currentJava)), 0o644); err != nil {
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
	cfg, err := config.Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := config.DisplayName(cfg.Apps["java-openjdk-21.0.11_p10"].Body, ""); got != "Java openjdk 21.0.11_p10" {
		t.Fatalf("loaded display name = %q, want no static current marker", got)
	}

	apps := List(context.Background(), testRunner{
		currentJava: {Stderr: "openjdk version \"21.0.11\" 2026-04-21 LTS\n", ExitCode: 0},
		java21:      {Stderr: "openjdk version \"21.0.11\" 2026-04-21 LTS\n", ExitCode: 0},
		java25:      {Stderr: "openjdk version \"25.0.3\" 2026-04-21 LTS\n", ExitCode: 0},
	}, cfg, config.CategoryApp, false)
	byName := map[string]Report{}
	for _, app := range apps {
		byName[app.Name] = app
	}
	if got := byName["java-openjdk-21.0.11_p10"].DisplayName; got != "Java openjdk 21.0.11_p10 current" {
		t.Fatalf("java 21 display name = %q, want current marker", got)
	}
	if got := byName["java-openjdk-25.0.3_p9"].DisplayName; got != "Java openjdk 25.0.3_p9" {
		t.Fatalf("java 25 display name = %q, want no current marker", got)
	}
}

func TestInspectCanTreatVersionFailureAsOptional(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "webd")
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := config.Resolved{Tree: map[string]any{
		"preflight": map[string]any{
			"binary":  map[string]any{"type": "binary", "path": binary},
			"version": map[string]any{"type": "command", "command": []any{binary, "--version"}},
		},
	}}
	runner := testRunner{binary: {Stderr: "bad flag\n", ExitCode: 2}}

	strict := Inspect(context.Background(), runner, "web", resolved)
	if strict.OK || strict.Status == "ok" {
		t.Fatalf("strict Inspect() = %+v, want version failure", strict)
	}
	if !strings.Contains(strict.Output, "bad flag") {
		t.Fatalf("a failing probe must capture the command output, got %q", strict.Output)
	}
	optional := Inspect(context.Background(), runner, "web", resolved, WithOptionalVersion())
	if !optional.OK || optional.Status != "ok" {
		t.Fatalf("optional Inspect() = %+v, want ok with unknown version", optional)
	}

	resolved.Tree["version_match"] = map[string]any{"contains": "Webd"}
	matched := Inspect(context.Background(), runner, "web", resolved, WithOptionalVersion())
	if matched.Installed || !strings.HasPrefix(matched.Status, "not installed: version ") {
		t.Fatalf("version_match Inspect() = %+v, want identity failure despite optional version", matched)
	}
}

func TestShortVersionForIgnoresFailedCommand(t *testing.T) {
	tree := map[string]any{
		"commands": map[string]any{
			"version_short": map[string]any{"command": []any{"/bin/tool", "--short"}},
		},
	}

	// A version_short command that exits non-zero must NOT have its (garbage)
	// output trusted; fall back to parsing the raw version line.
	failing := testRunner{"/bin/tool": {Stdout: "ERROR: bad usage\n", ExitCode: 1}}
	if got := shortVersionFor(context.Background(), failing, tree, "Webd 1.2.3"); got != "1.2.3" {
		t.Fatalf("shortVersionFor on failed command = %q, want fallback 1.2.3", got)
	}

	// A successful command's first line is trusted verbatim.
	ok := testRunner{"/bin/tool": {Stdout: "2.0.0\n", ExitCode: 0}}
	if got := shortVersionFor(context.Background(), ok, tree, "Webd 1.2.3"); got != "2.0.0" {
		t.Fatalf("shortVersionFor on success = %q, want 2.0.0", got)
	}
}

func TestProbeCommandFor(t *testing.T) {
	tree := map[string]any{
		"commands": map[string]any{
			"version": map[string]any{
				"command":       []any{"/bin/tool", "--version"},
				"user":          "postgres",
				"timeout":       "10s",
				"expect_exit":   3,
				"expect_stdout": "v1.",
				"expect_stderr": map[string]any{"op": "==", "value": ""},
			},
		},
	}
	vc := probeCommandFor(tree, "version")
	if len(vc.argv) != 2 || vc.argv[0] != "/bin/tool" {
		t.Fatalf("argv = %v", vc.argv)
	}
	if vc.user != "postgres" {
		t.Errorf("user = %q, want postgres", vc.user)
	}
	if vc.timeout != 10*time.Second {
		t.Errorf("timeout = %v, want 10s", vc.timeout)
	}
	if !slices.Equal(vc.expectExit, []int{3}) {
		t.Errorf("expectExit = %v, want [3]", vc.expectExit)
	}
	if vc.stdout.Substring != "v1." {
		t.Errorf("stdout matcher = %+v, want substring v1.", vc.stdout)
	}
	if vc.stderr.Op != "==" {
		t.Errorf("stderr matcher = %+v, want op ==", vc.stderr)
	}
}

type timeoutObserver struct {
	timeout time.Duration
}

func (o *timeoutObserver) Run(ctx context.Context, name string, _ ...string) (execx.Result, error) {
	if deadline, ok := ctx.Deadline(); ok {
		o.timeout = time.Until(deadline)
	}
	return execx.Result{Stdout: "tool 1.2.3\n", ExitCode: 0}, nil
}

func TestInspectUsesCatalogProbeTimeout(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "salt-minion")
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := config.Resolved{Tree: map[string]any{
		"preflight": map[string]any{
			"binary":  map[string]any{"type": "binary", "path": binary},
			"version": map[string]any{"type": "command", "command": []any{binary, "--version"}, "timeout": "10s"},
		},
	}}
	obs := &timeoutObserver{}
	report := Inspect(context.Background(), obs, "salt-minion", resolved)
	if !report.OK || report.VersionShort != "1.2.3" {
		t.Fatalf("Inspect() = %+v, want ok version 1.2.3", report)
	}
	if obs.timeout < 9*time.Second || obs.timeout > 10*time.Second {
		t.Fatalf("probe timeout = %v, want ~10s from catalog entry", obs.timeout)
	}
}

type slowRunner struct{}

func (slowRunner) Run(ctx context.Context, _ string, _ ...string) (execx.Result, error) {
	<-ctx.Done()
	return execx.Result{ExitCode: -1}, fmt.Errorf("run tool: %w", ctx.Err())
}

func TestInspectProbeTimeoutFailureReportsUnderlyingError(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "slow-tool")
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := config.Resolved{Tree: map[string]any{
		"preflight": map[string]any{
			"binary":  map[string]any{"type": "binary", "path": binary},
			"version": map[string]any{"type": "command", "command": []any{binary, "--version"}, "timeout": "1ms"},
		},
	}}
	report := Inspect(context.Background(), slowRunner{}, "slow-tool", resolved)
	if report.OK {
		t.Fatalf("Inspect() = %+v, want version probe failure", report)
	}
	if !strings.Contains(report.Status, "timeout after 1ms") {
		t.Fatalf("status = %q, want timeout after duration instead of exit -1", report.Status)
	}
}

// TestShortVersionRealData exercises ShortVersion against version strings
// captured live from production hosts (bk1, fw1, kvm5, kvm9, radon — June 2026)
// by running each app's configured version command and taking the first
// non-empty line, exactly as Inspect does. Each line must reduce to its numeric
// version and at most the patchlevel; a miss fails the test so the regex can be
// tightened against formats it does not yet cover.
func TestShortVersionRealData(t *testing.T) {
	cases := []struct {
		app, raw, want string
	}{
		{"acpid", "acpid-2.0.34", "2.0.34"},
		{"asterisk", "Asterisk 13.38.3", "13.38.3"},
		{"automount", "Linux automount version 5.1.9", "5.1.9"},
		{"bash", "GNU bash, versión 5.3.9(1)-release (x86_64-pc-linux-gnu)", "5.3.9"},
		{"certbot", "certbot 5.5.0", "5.5.0"},
		{"certbot", "certbot 4.0.0", "4.0.0"},
		{"clamd", "ClamAV 1.5.2/27673/Wed Jun 18 11:48:55 2025", "1.5.2"},
		{"clamd", "ClamAV 1.4.3", "1.4.3"},
		{"dbus-daemon", "D-Bus Message Bus Daemon 1.16.2", "1.16.2"},
		{"dhclient", "isc-dhclient-4.4.3-P1 Gentoo-r6", "4.4.3-P1"},
		{"dhcpd", "isc-dhcpd-4.4.3-P1 Gentoo-r6", "4.4.3-P1"},
		{"dmeventd", "dmeventd version: 1.02.213 (2026-03-13)", "1.02.213"},
		{"dovecot", "2.3.21.1 (d492236fa0)", "2.3.21"},
		{"exim", "Exim version 4.98.2 #2 built 25-Feb-2026 20:07:53", "4.98.2"},
		{"fail2ban-server", "Fail2Ban v1.1.0", "1.1.0"},
		{"fcron", "fcron 3.4.0 - periodic command scheduler", "3.4.0"},
		{"grafana", "grafana version 12.4.3+security-02", "12.4.3"},
		{"git", "git version 2.53.0", "2.53.0"},
		{"go", "go version go1.26.2-X:nodwarf5 linux/amd64", "1.26.2"},
		{"guacd", "Guacamole proxy daemon (guacd) version 1.6.0", "1.6.0"},
		{"in.tftpd", "tftp-hpa 5.2, with remap, without tcpwrappers", "5.2"},
		{"java", `openjdk version "21.0.11" 2026-04-21 LTS`, "21.0.11"},
		{"libvirtd", "/usr/bin/libvirtd (libvirt) 12.0.0", "12.0.0"},
		{"lvm", "  LVM version:     2.03.39(2) (2026-03-13)", "2.03.39"},
		{"lvmpolld", "lvmpolld version: 2.03.39(2) (2026-03-13)", "2.03.39"},
		{"mail", "s-nail v14.9.25, 2024-06-27 (built for Linux)", "14.9.25"},
		{"mariadb", "/usr/bin/mariadbd  Ver 11.8.6-MariaDB-log for Linux on x86_64 (Source distribution)", "11.8.6"},
		{"mariadb", "/usr/bin/mariadbd  Ver 11.4.7-MariaDB for Linux on x86_64 (Source distribution)", "11.4.7"},
		{"mdadm", "mdadm - v4.6 - 2026-03-16", "4.6"},
		{"mysql", "/usr/bin/mysqld  Ver 11.8.3-MariaDB for Linux on x86_64 (Source distribution)", "11.8.3"},
		{"named", "BIND 9.18.49 (Extended Support Version) <id:cd4a53b>", "9.18.49"},
		{"nginx", "nginx version: nginx/1.30.2", "1.30.2"},
		{"nmbd", "Version 4.22.3", "4.22.3"},
		{"node", "v24.14.0", "24.14.0"},
		{"numad", "/usr/bin/numad version: 20150602: compiled Sep 12 2024", "20150602"},
		{"ntpd", "ntpd 4.2.8p18", "4.2.8p18"},
		{"ntpd", "ntpd 4.2.8p18@1.4062-o Thu May 14 07:09:36 UTC 2026 (1)", "4.2.8p18"},
		{"openssl", "OpenSSL 3.5.6 7 Apr 2026 (Library: OpenSSL 3.5.6 7 Apr 2026)", "3.5.6"},
		{"openssh", "OpenSSH_9.9p2, OpenSSL 3.3.3 11 Feb 2025", "9.9p2"},
		{"openvpn", "OpenVPN 2.6.20 x86_64-pc-linux-gnu [SSL (OpenSSL)] [LZO] [LZ4] [EPOLL] [MH/PKTINFO] [AEAD]", "2.6.20"},
		{"ovs-vswitchd", "ovs-vswitchd (Open vSwitch) 3.7.1", "3.7.1"},
		{"ovsdb-client", "ovsdb-client (Open vSwitch) 3.7.1", "3.7.1"},
		{"perl", "This is perl 5, version 42, subversion 0 (v5.42.0) built for x86_64-linux-thread-multi", "5.42.0"},
		{"php", "PHP 8.2.31 (cli) (built: May 25 2026 20:34:19) (NTS)", "8.2.31"},
		{"php", "PHP 5.6.40-pl0-gentoo (cli) (built: Sep  5 2025 19:03:34) ", "5.6.40"},
		{"pkexec", "pkexec version 126", "126"},
		{"proftpd", "ProFTPD Version 1.3.9a", "1.3.9"},
		{"python", "Python 3.13.13", "3.13.13"},
		{"qemu-ga", "QEMU Guest Agent 9.2.0", "9.2.0"},
		{"redis", "Redis server v=8.2.6 sha=00000000:1 malloc=tcmalloc-2.15 bits=64 build=8c3fa9ca5bd0100b", "8.2.6"},
		{"rpc-mountd", "rpc.mountd version 2.8.5", "2.8.5"},
		{"rpc-statd", "rpc.statd version 2.8.5", "2.8.5"},
		{"rspamd", "Rspamd daemon version 4.0.1", "4.0.1"},
		{"rsync", "rsync  version 3.4.3  protocol version 32", "3.4.3"},
		{"ruby", "ruby 3.3.11 (2026-03-26 revision 1f2d15125a) [x86_64-linux]", "3.3.11"},
		{"salt", "salt 3007.14 (Chlorine)", "3007.14"},
		{"smartd", "smartd 7.5 2025-04-30 r5714 [x86_64-linux-6.12.74-gentoo] (local build)", "7.5"},
		{"smbd", "Version 4.22.3", "4.22.3"},
		{"snmpd", "NET-SNMP version:  5.9.5.2", "5.9.5.2"},
		{"snmpd", "NET-SNMP version:  5.9.4.pre", "5.9.4.pre"},
		{"snmpd", "NET-SNMP version:  5.9.4.pre2", "5.9.4.pre2"},
		{"spamassassin", "SpamAssassin version 4.0.1", "4.0.1"},
		{"sqlite", "3.51.3 2026-03-13 10:38:09 737ae4a34738ffa0c3ff7f9bb18df914dd1cad163f28fd6b6e114a344fe6alt1 (64-bit)", "3.51.3"},
		{"squid", "Squid Cache: Version 6.14", "6.14"},
		{"ssh", "OpenSSH_10.3p1, OpenSSL 3.5.6 7 Apr 2026", "10.3p1"},
		{"syslog-ng", "syslog-ng 4 (4.10.2)", "4.10.2"},
		{"systemd", "systemd 260 (260.1)", "260.1"},
		{"upsd", "Network UPS Tools upsd 2.8.2.1 (development iteration after 2.8.2)", "2.8.2.1"},
		{"upsd", "Network UPS Tools upsd 2.8.4.1-0+gc7198d501 (development iteration after 2.8.4)", "2.8.4.1"},
		{"upsmon", "Network UPS Tools upsmon 2.8.2.1 (development iteration after 2.8.2)", "2.8.2.1"},
		{"upsmon", "Network UPS Tools upsmon 2.8.4.1-0+gc7198d501 (development iteration after 2.8.4)", "2.8.4.1"},
		{"virtlockd", "/usr/bin/virtlockd (libvirt) 12.0.0", "12.0.0"},
		{"xinetd", "xinetd 2.3.15.4", "2.3.15.4"},
	}

	for _, c := range cases {
		got := ShortVersion(c.raw)
		if got == "" {
			t.Errorf("%s: ShortVersion(%q) found no version; adjust shortVersionRE to cover this format", c.app, c.raw)
			continue
		}
		if got != c.want {
			t.Errorf("%s: ShortVersion(%q) = %q, want %q", c.app, c.raw, got, c.want)
		}
	}
}

// TestShortVersionNoVersion documents lines that legitimately carry no version
// number: ShortVersion returns "" rather than inventing one. Such a line is what
// proftpd's old `-V` command emitted ("Compile-time Settings:"); its catalog
// entry now uses `--version`, which reports a parseable "ProFTPD Version 1.3.9a".
func TestShortVersionNoVersion(t *testing.T) {
	for _, raw := range []string{
		"",
		"Compile-time Settings:",
		"no version here",
		"built for x86_64-linux-thread-multi",
	} {
		if got := ShortVersion(raw); got != "" {
			t.Errorf("ShortVersion(%q) = %q, want empty", raw, got)
		}
	}
}
