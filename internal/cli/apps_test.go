package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/execx"
)

func TestAppVersionCommandExpectations(t *testing.T) {
	tree := map[string]any{
		"commands": map[string]any{
			"version": map[string]any{
				"command":       []any{"/bin/tool", "--version"},
				"expect_exit":   3,
				"expect_stdout": "v1.",
				"expect_stderr": map[string]any{"op": "==", "value": ""},
			},
		},
	}
	vc := appVersionCommand(tree, "version")
	if len(vc.argv) != 2 || vc.argv[0] != "/bin/tool" {
		t.Fatalf("argv = %v", vc.argv)
	}
	if vc.expectExit != 3 {
		t.Errorf("expectExit = %d, want 3", vc.expectExit)
	}
	if vc.stdout.Substring != "v1." {
		t.Errorf("stdout matcher = %+v, want substring v1.", vc.stdout)
	}
	if vc.stderr.Op != "==" {
		t.Errorf("stderr matcher = %+v, want op ==", vc.stderr)
	}
}

// TestShortVersionRealData exercises shortVersion against version strings
// captured live from production hosts (bk1, fw1, kvm5, kvm9, radon — June 2026)
// by running each app's configured version command and taking the first
// non-empty line, exactly as inspectApp does. Each line must reduce to its
// numeric version and at most the patchlevel; a miss fails the test so the
// regex can be tightened against formats it does not yet cover.
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
		{"dhclient", "isc-dhclient-4.4.3-P1 Gentoo-r6", "4.4.3"},
		{"dhcpd", "isc-dhcpd-4.4.3-P1 Gentoo-r6", "4.4.3"},
		{"dmeventd", "dmeventd version: 1.02.213 (2026-03-13)", "1.02.213"},
		{"dovecot", "2.3.21.1 (d492236fa0)", "2.3.21"},
		{"exim", "Exim version 4.98.2 #2 built 25-Feb-2026 20:07:53", "4.98.2"},
		{"fail2ban-server", "Fail2Ban v1.1.0", "1.1.0"},
		{"fcron", "fcron 3.4.0 - periodic command scheduler", "3.4.0"},
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
		{"ntpd", "ntpd 4.2.8p18@1.4062-o Thu May 14 07:09:36 UTC 2026 (1)", "4.2.8"},
		{"openssl", "OpenSSL 3.5.6 7 Apr 2026 (Library: OpenSSL 3.5.6 7 Apr 2026)", "3.5.6"},
		{"openvpn", "OpenVPN 2.6.20 x86_64-pc-linux-gnu [SSL (OpenSSL)] [LZO] [LZ4] [EPOLL] [MH/PKTINFO] [AEAD]", "2.6.20"},
		{"ovs-vswitchd", "ovs-vswitchd (Open vSwitch) 3.7.1", "3.7.1"},
		{"ovsdb-client", "ovsdb-client (Open vSwitch) 3.7.1", "3.7.1"},
		{"perl", "This is perl 5, version 42, subversion 0 (v5.42.0) built for x86_64-linux-thread-multi", "5.42.0"},
		{"php", "PHP 8.2.31 (cli) (built: May 25 2026 20:34:19) (NTS)", "8.2.31"},
		{"php", "PHP 5.6.40-pl0-gentoo (cli) (built: Sep  5 2025 19:03:34) ", "5.6.40"},
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
		{"snmpd", "NET-SNMP version:  5.9.5.2", "5.9.5"},
		{"snmpd", "NET-SNMP version:  5.9.4.pre2", "5.9.4"},
		{"spamassassin", "SpamAssassin version 4.0.1", "4.0.1"},
		{"sqlite", "3.51.3 2026-03-13 10:38:09 737ae4a34738ffa0c3ff7f9bb18df914dd1cad163f28fd6b6e114a344fe6alt1 (64-bit)", "3.51.3"},
		{"squid", "Squid Cache: Version 6.14", "6.14"},
		{"ssh", "OpenSSH_10.3p1, OpenSSL 3.5.6 7 Apr 2026", "10.3"},
		{"syslog-ng", "syslog-ng 4 (4.10.2)", "4.10.2"},
		{"systemd", "systemd 260 (260.1)", "260.1"},
		{"upsd", "Network UPS Tools upsd 2.8.4.1-0+gc7198d501 (development iteration after 2.8.4)", "2.8.4"},
		{"upsmon", "Network UPS Tools upsmon 2.8.4.1-0+gc7198d501 (development iteration after 2.8.4)", "2.8.4"},
		{"virtlockd", "/usr/bin/virtlockd (libvirt) 12.0.0", "12.0.0"},
		{"xinetd", "2.3.15.4 loadavg", "2.3.15"},
	}

	for _, c := range cases {
		got := shortVersion(c.raw)
		if got == "" {
			t.Errorf("%s: shortVersion(%q) found no version; adjust shortVersionRE to cover this format", c.app, c.raw)
			continue
		}
		if got != c.want {
			t.Errorf("%s: shortVersion(%q) = %q, want %q", c.app, c.raw, got, c.want)
		}
	}
}

// TestShortVersionNoVersion documents lines that legitimately carry no version
// number: shortVersion returns "" rather than inventing one. Such a line is what
// proftpd's old `-V` command emitted ("Compile-time Settings:"); its catalog
// entry now uses `--version`, which reports a parseable "ProFTPD Version 1.3.9a".
func TestShortVersionNoVersion(t *testing.T) {
	for _, raw := range []string{
		"",
		"Compile-time Settings:",
		"no version here",
		"built for x86_64-linux-thread-multi",
	} {
		if got := shortVersion(raw); got != "" {
			t.Errorf("shortVersion(%q) = %q, want empty", raw, got)
		}
	}
}

// TestAppsVersionShortCommand checks how version_short is sourced: a daemon that
// configures a `version_short` command has its bare output trusted verbatim (no
// regex), while one without falls back to parsing the raw version line, and a
// configured command that prints nothing also falls back.
func TestAppsVersionShortCommand(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// One binary per app; the version_short programs are separate paths the
	// fakeRunner keys on and need not exist on disk (they are never stat'd).
	native := filepath.Join(binDir, "native")
	fallback := filepath.Join(binDir, "fallback")
	empty := filepath.Join(binDir, "empty")
	for _, p := range []string{native, fallback, empty} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	daemonsDir := filepath.Join(root, "daemons")
	appsDir := filepath.Join(daemonsDir, "apps")
	enabledDir := filepath.Join(root, "enabled")
	for _, d := range []string{appsDir, enabledDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// nativeapp: a version_short command prints the bare version directly.
	if err := os.WriteFile(filepath.Join(appsDir, "native.yml"), []byte(fmt.Sprintf(`kind: app
name: nativeapp
display_name: "NativeApp"
variables: { binary: %q, shortprog: %q }
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}","--version"], timeout: 10s }
  version_short: { type: command, command: ["${shortprog}"], timeout: 10s }
`, native, native+"-vs")), 0o644); err != nil {
		t.Fatal(err)
	}
	// fallbackapp: no version_short command — parse the raw version line.
	if err := os.WriteFile(filepath.Join(appsDir, "fallback.yml"), []byte(fmt.Sprintf(`kind: app
name: fallbackapp
display_name: "FallbackApp"
variables: { binary: %q }
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}","--version"], timeout: 10s }
`, fallback)), 0o644); err != nil {
		t.Fatal(err)
	}
	// emptyapp: version_short command runs but prints nothing — fall back.
	if err := os.WriteFile(filepath.Join(appsDir, "empty.yml"), []byte(fmt.Sprintf(`kind: app
name: emptyapp
display_name: "EmptyApp"
variables: { binary: %q, shortprog: %q }
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}","--version"], timeout: 10s }
  version_short: { type: command, command: ["${shortprog}"], timeout: 10s }
`, empty, empty+"-vs")), 0o644); err != nil {
		t.Fatal(err)
	}

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], includes: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, daemonsDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := fakeRunner{byPath: map[string]execx.Result{
		// Raw version lines whose regex-short would differ from the command,
		// so the assertions distinguish the two sources.
		native:         {Stdout: "NativeApp 4.5.6-7-extra\n", ExitCode: 0},
		native + "-vs": {Stdout: "4.5\n", ExitCode: 0}, // verbatim, != regex "4.5.6"
		fallback:       {Stdout: "FallbackApp 9.8.7.6\n", ExitCode: 0},
		empty:          {Stdout: "EmptyApp 3.2.1.0\n", ExitCode: 0},
		empty + "-vs":  {Stdout: "", ExitCode: 0}, // prints nothing -> fall back
	}}

	var jstdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &jstdout, Stderr: &bytes.Buffer{}, Runner: runner}
	if code := app.Run(context.Background(), []string{"--config", global, "--json", "apps"}); code != exitSuccess {
		t.Fatalf("apps --json exit = %d", code)
	}
	js := jstdout.String()
	// nativeapp: trusts the command output (4.5), not the regex (4.5.6).
	if !strings.Contains(js, `"version_short":"4.5"`) {
		t.Errorf("nativeapp should use version_short command output 4.5:\n%s", js)
	}
	if strings.Contains(js, `"version_short":"4.5.6"`) {
		t.Errorf("nativeapp must not fall back to regex when version_short command is set:\n%s", js)
	}
	// fallbackapp: regex on "9.8.7.6" keeps at most the patchlevel.
	if !strings.Contains(js, `"version_short":"9.8.7"`) {
		t.Errorf("fallbackapp should parse short version 9.8.7:\n%s", js)
	}
	// emptyapp: empty command output falls back to regex on "3.2.1.0".
	if !strings.Contains(js, `"version_short":"3.2.1"`) {
		t.Errorf("emptyapp should fall back to regex 3.2.1:\n%s", js)
	}
}

// fakeRunner answers version-command invocations keyed by the binary path.
type fakeRunner struct{ byPath map[string]execx.Result }

func (f fakeRunner) Run(_ context.Context, name string, _ ...string) (execx.Result, error) {
	if r, ok := f.byPath[name]; ok {
		return r, nil
	}
	return execx.Result{ExitCode: 127}, fmt.Errorf("%s: not found", name)
}

func TestAppsCommand(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(binDir, "good")
	bad := filepath.Join(binDir, "bad")
	missing := filepath.Join(binDir, "missing") // never created
	for _, p := range []string{good, bad} {
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	daemonsDir := filepath.Join(root, "daemons")
	appsDir := filepath.Join(daemonsDir, "apps") // category derived from the directory
	enabledDir := filepath.Join(root, "enabled")
	for _, d := range []string{appsDir, enabledDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeDaemon := func(file, name, display, binary string) {
		body := fmt.Sprintf(`kind: daemon
name: %s
display_name: %q
service: { name: %s }
variables:
  binary: %q
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}","--version"], timeout: 10s }
`, name, display, name, binary)
		if err := os.WriteFile(filepath.Join(appsDir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeDaemon("good.yml", "goodapp", "GoodApp", good)
	writeDaemon("bad.yml", "badapp", "BadApp", bad)
	writeDaemon("gone.yml", "goneapp", "GoneApp", missing)

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(fmt.Sprintf(`
engine: { backend: auto }
paths: { catalog: [ %s ], includes: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, daemonsDir, enabledDir)), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := fakeRunner{byPath: map[string]execx.Result{
		good: {Stdout: "GoodApp 1.2.3\n", ExitCode: 0},
		bad:  {Stderr: "boom\n", ExitCode: 3},
	}}

	run := func(args ...string) string {
		var stdout bytes.Buffer
		app := App{
			Env:    func(string) string { return "" },
			Stdout: &stdout,
			Stderr: &bytes.Buffer{},
			Runner: runner,
		}
		if code := app.Run(context.Background(), append([]string{"--config", global}, args...)); code != exitSuccess {
			t.Fatalf("apps %v exit = %d", args, code)
		}
		return stdout.String()
	}

	// Default: only installed apps, the short version, and status.
	out := run("apps")
	if !strings.Contains(out, "GoodApp") || !strings.Contains(out, "1.2.3") || !strings.Contains(out, "ok") {
		t.Errorf("apps missing good app row:\n%s", out)
	}
	if strings.Contains(out, "GoodApp 1.2.3") {
		t.Errorf("apps should show the short version by default, not the raw string:\n%s", out)
	}
	if !strings.Contains(out, "BadApp") || !strings.Contains(out, "exit 3 (want 0): boom") {
		t.Errorf("apps missing bad app error:\n%s", out)
	}
	if strings.Contains(out, "GoneApp") {
		t.Errorf("apps should hide not-installed app by default:\n%s", out)
	}

	// `apps --long` shows the full raw version string instead.
	outLong := run("apps", "--long")
	if !strings.Contains(outLong, "GoodApp 1.2.3") {
		t.Errorf("apps --long should show the full version string:\n%s", outLong)
	}

	// `apps all` also lists the not-installed app.
	outAll := run("apps", "all")
	if !strings.Contains(outAll, "GoneApp") || !strings.Contains(outAll, "not installed") {
		t.Errorf("apps all should list not-installed app:\n%s", outAll)
	}

	// JSON carries the structured fields.
	var jstdout bytes.Buffer
	app := App{Env: func(string) string { return "" }, Stdout: &jstdout, Stderr: &bytes.Buffer{}, Runner: runner}
	if code := app.Run(context.Background(), []string{"--config", global, "--json", "apps"}); code != exitSuccess {
		t.Fatalf("apps --json exit = %d", code)
	}
	js := jstdout.String()
	if !strings.Contains(js, `"version":"GoodApp 1.2.3"`) || !strings.Contains(js, `"version_short":"1.2.3"`) || !strings.Contains(js, `"installed":true`) || !strings.Contains(js, `"ok":false`) {
		t.Errorf("apps --json unexpected:\n%s", js)
	}
}
