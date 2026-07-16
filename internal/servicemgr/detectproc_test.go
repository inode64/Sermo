package servicemgr

import (
	"context"
	"testing"

	"sermo/internal/execx"
)

// assertDetectSystemd builds a fakeRunner from results, runs the systemd detector
// for unit and asserts the exact pidfile/exe pair.
func assertDetectSystemd(t *testing.T, results map[string]execx.Result, unit, wantPidfile, wantExe string) {
	t.Helper()
	runner := fakeRunner{results: results}
	pidfile, exe := detectProcPidfileExe(context.Background(), runner, nil, BackendSystemd, unit)
	if pidfile != wantPidfile {
		t.Fatalf("pidfile = %q, want %q", pidfile, wantPidfile)
	}
	if exe != wantExe {
		t.Fatalf("exe = %q, want %q", exe, wantExe)
	}
}

func TestDetectProcSystemd(t *testing.T) {
	assertDetectSystemd(t, map[string]execx.Result{
		"systemctl show -p PIDFile --value -- nginx.service":   {Stdout: "/run/nginx.pid\n"},
		"systemctl show -p ExecStart --value -- nginx.service": {Stdout: "{ path=/usr/sbin/nginx ; argv[]=/usr/sbin/nginx -g daemon off ; ignore_errors=no }\n"},
	}, "nginx.service", "/run/nginx.pid", "/usr/sbin/nginx")
}

func TestDetectProcSystemdExecStartOnly(t *testing.T) {
	// No PIDFile= in the unit: pidfile is empty, but the exe is still derived.
	assertDetectSystemd(t, map[string]execx.Result{
		"systemctl show -p ExecStart --value -- sshd.service": {Stdout: "{ path=/usr/sbin/sshd ; argv[]=/usr/sbin/sshd -D }\n"},
	}, "sshd.service", "", "/usr/sbin/sshd")
}

func TestDetectProcSystemdNormalizesLegacyVarRun(t *testing.T) {
	assertDetectSystemd(t, map[string]execx.Result{
		"systemctl show -p PIDFile --value -- apache.service":   {Stdout: "/var/run/apache2.pid\n"},
		"systemctl show -p ExecStart --value -- apache.service": {Stdout: "{ path=/usr/sbin/apache2 ; argv[]=/usr/sbin/apache2 -k start ; ignore_errors=no }\n"},
	}, "apache.service", "/run/apache2.pid", "/usr/sbin/apache2")
}

func TestDetectProcOpenRCPidfile(t *testing.T) {
	read := fakeReadFile("/etc/init.d/nginx", "#!/sbin/openrc-run\ncommand=\"/usr/sbin/nginx\"\npidfile=\"/run/nginx.pid\"\n")
	pidfile, exe := detectProcPidfileExe(context.Background(), nil, read, BackendOpenRC, "nginx")
	if pidfile != "/run/nginx.pid" {
		t.Fatalf("pidfile = %q, want /run/nginx.pid", pidfile)
	}
	if exe != "/usr/sbin/nginx" {
		t.Fatalf("exe = %q, want /usr/sbin/nginx", exe)
	}
}

func TestDetectProcOpenRCStartStopDaemonArg(t *testing.T) {
	// pidfile passed as a start-stop-daemon argument, command in conf.d.
	read := fakeReadFile("/etc/init.d/foo", "start() {\n  start-stop-daemon --start --pidfile /run/foo/foo.pid --exec /usr/bin/foo\n}\n")
	pidfile, _ := detectProcPidfileExe(context.Background(), nil, read, BackendOpenRC, "foo")
	if pidfile != "/run/foo/foo.pid" {
		t.Fatalf("pidfile = %q, want /run/foo/foo.pid", pidfile)
	}
}

func TestDetectProcOpenRCCleansAbsolutePaths(t *testing.T) {
	assertDetectProc(t, "dhcpd", map[string]string{
		"/etc/init.d/dhcpd": `pidfile="//run/dhcp/dhcpd.pid"
command="//usr/sbin/dhcpd"
`,
	}, ProcInfo{
		Pidfile: "/run/dhcp/dhcpd.pid",
		Exe:     "/usr/sbin/dhcpd",
		Cmd:     `(^|[[:space:]])/usr/sbin/dhcpd($|[[:space:]])`,
	})
}

func TestDetectProcNormalizesLegacyVarRun(t *testing.T) {
	assertDetectProc(t, "apache2", map[string]string{
		"/etc/init.d/apache2": `pidfile="/var/run/apache2.pid"
command="/usr/sbin/apache2"
`,
	}, ProcInfo{Pidfile: "/run/apache2.pid"})
}

func TestDetectProcOpenRCSkipsNonAbsoluteExec(t *testing.T) {
	read := func(path string) ([]byte, error) {
		if path == "/etc/init.d/net.eth0" {
			return []byte(`command="' config_index='"
start-stop-daemon --start --exec ' config_index='
`), nil
		}
		return nil, errNotFound
	}
	info := DetectProcInfo(context.Background(), nil, read, BackendOpenRC, "net.eth0")
	if info.Exe != "" || info.Cmd != "" {
		t.Fatalf("Exe/Cmd = %q/%q, want empty for non-absolute executable", info.Exe, info.Cmd)
	}
}

func TestDetectProcOpenRCApacheGentooDefaults(t *testing.T) {
	assertDetectProc(t, "apache2", map[string]string{
		"/etc/init.d/apache2": `PIDFILE="${PIDFILE:-/run/apache2.pid}"
APACHE2="/usr/sbin/apache2"
start() {
	start-stop-daemon --start --pidfile "${PIDFILE}" -- \
		${APACHE2} ${APACHE2_OPTS} -k start
}
`,
	}, ProcInfo{Pidfile: "/run/apache2.pid", Exe: "/usr/sbin/apache2"})
}

func TestDetectProcOpenRCExpandsServiceNameAndEmptyRoots(t *testing.T) {
	read := func(path string) ([]byte, error) {
		if path == "/etc/init.d/sshd" {
			return []byte(`: ${SSHD_PIDFILE:=${RC_PREFIX%/}/run/${SVCNAME}.pid}
: ${SSHD_BINARY:=${RC_PREFIX%/}/usr/sbin/sshd}
command="${SSHD_BINARY}"
pidfile="${SSHD_PIDFILE}"
command_args="${SSHD_OPTS} -o PidFile=${pidfile}"
`), nil
		}
		return nil, errNotFound
	}
	info := DetectProcInfo(context.Background(), nil, read, BackendOpenRC, "sshd")
	if info.Pidfile != "/run/sshd.pid" {
		t.Fatalf("pidfile = %q, want /run/sshd.pid", info.Pidfile)
	}
	if info.Exe != "/usr/sbin/sshd" {
		t.Fatalf("exe = %q, want /usr/sbin/sshd", info.Exe)
	}
	if info.Cmd == "" {
		t.Fatal("cmd fallback should be derived from command")
	}
}

func TestDetectProcOpenRCPrefixRemoval(t *testing.T) {
	assertDetectProc(t, "php8.2", map[string]string{
		"/etc/init.d/php8.2": `PHP_SLOT="${SVCNAME#php-fpm-}"
command="/usr/lib64/${PHP_SLOT}/bin/php-fpm"
pidfile="/run/php-fpm-${PHP_SLOT}.pid"
`,
	}, ProcInfo{
		Pidfile: "/run/php-fpm-php8.2.pid",
		Exe:     "/usr/lib64/php8.2/bin/php-fpm",
	})
}

func TestDetectProcOpenRCPatternPrefixRemoval(t *testing.T) {
	assertDetectProc(t, "openvpn.tun1", map[string]string{
		"/etc/init.d/openvpn.tun1": `VPN=${SVCNAME#*.}
if [ -n "${VPN}" ] && [ ${SVCNAME} != "openvpn" ]; then
	VPNPID="/run/openvpn.${VPN}.pid"
else
	VPNPID="/run/openvpn.pid"
fi
start-stop-daemon --start --exec /usr/sbin/openvpn -- \
	--config "/etc/openvpn/${VPN}.conf" --writepid "${VPNPID}" --daemon
`,
	}, ProcInfo{
		Pidfile: "/run/openvpn.tun1.pid",
		Exe:     "/usr/sbin/openvpn",
	})
}

func TestDetectProcOpenRCChrootDefaultsToHostRoot(t *testing.T) {
	assertDetectProc(t, "named", map[string]string{
		"/etc/init.d/named": `PIDFILE="${CHROOT}/run/named/named.pid"
start-stop-daemon --start --pidfile ${PIDFILE} --exec /usr/sbin/named
`,
	}, ProcInfo{
		Pidfile: "/run/named/named.pid",
		Exe:     "/usr/sbin/named",
	})
}

func TestDetectProcOpenRCCommandUser(t *testing.T) {
	assertDetectProc(t, "influxdb", map[string]string{
		"/etc/init.d/influxdb": `user=${user:-influxdb}
group=${group:-influxdb}
command=/usr/bin/influxd
command_user="${user}:${group}"
`,
	}, ProcInfo{
		Exe:  "/usr/bin/influxd",
		User: "influxdb",
		Cmd:  `(^|[[:space:]])/usr/bin/influxd($|[[:space:]])`,
	})
}

func TestDetectProcOpenRCRuntimeOptionsFallback(t *testing.T) {
	assertDetectProc(t, "mysql", map[string]string{
		"/etc/init.d/mysql": `MY_CNF="${MY_CNF:-/etc/${SVCNAME}/my.cnf}"
start() {
	pidfile=$(get_config "${MY_CNF}" 'pid[_-]file' | tail -n1)
	start-stop-daemon --start --exec /usr/sbin/mysqld --pidfile "${pidfile}"
	save_options pidfile "${pidfile}"
}
`,
		"/run/openrc/daemons/mysql/001": `exec=/usr/sbin/mysqld
argv_0=/usr/sbin/mysqld
argv_1=--defaults-file=/etc/mysql/my.cnf
pidfile=/run/mysqld/mariadb.pid
`,
	}, ProcInfo{
		Pidfile: "/run/mysqld/mariadb.pid",
		Exe:     "/usr/sbin/mysqld",
		Cmd:     `(^|[[:space:]])/usr/sbin/mysqld($|[[:space:]])`,
	})
}

func TestDetectProcOpenRCSkipsVariablePidfile(t *testing.T) {
	// A pidfile built from a variable is not a literal path; it must be skipped
	// rather than emitting a useless `pidfile: ${...}`.
	read := fakeReadFile("/etc/init.d/bar", "pidfile=\"${RUNTIME_DIR}/bar.pid\"\ncommand=/usr/bin/bar\n")
	pidfile, exe := detectProcPidfileExe(context.Background(), nil, read, BackendOpenRC, "bar")
	if pidfile != "" {
		t.Fatalf("pidfile = %q, want empty (variable, not literal)", pidfile)
	}
	if exe != "/usr/bin/bar" {
		t.Fatalf("exe = %q, want /usr/bin/bar", exe)
	}
}

// detectProcPidfileExe adapts DetectProcInfo to the pidfile/exe pair these tests
// assert on; production code calls DetectProcInfo directly.
func detectProcPidfileExe(ctx context.Context, runner execx.Runner, readFile func(string) ([]byte, error), backend Backend, unit string) (pidfile, exe string) {
	info := DetectProcInfo(ctx, runner, readFile, backend, unit)
	return info.Pidfile, info.Exe
}

var errNotFound = &fakeFSError{}

type fakeFSError struct{}

func (*fakeFSError) Error() string { return "not found" }

// fakeReadFile returns a readFile closure yielding body for path and errNotFound
// for any other path.
func fakeReadFile(path, body string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if p == path {
			return []byte(body), nil
		}
		return nil, errNotFound
	}
}

// assertDetectProc runs DetectProcInfo over the OpenRC init scripts in files
// and asserts every non-empty field of want; empty want fields are not checked.
func assertDetectProc(t *testing.T, name string, files map[string]string, want ProcInfo) {
	t.Helper()
	read := func(p string) ([]byte, error) {
		if body, ok := files[p]; ok {
			return []byte(body), nil
		}
		return nil, errNotFound
	}
	info := DetectProcInfo(context.Background(), nil, read, BackendOpenRC, name)
	if want.Pidfile != "" && info.Pidfile != want.Pidfile {
		t.Fatalf("pidfile = %q, want %q", info.Pidfile, want.Pidfile)
	}
	if want.Exe != "" && info.Exe != want.Exe {
		t.Fatalf("exe = %q, want %q", info.Exe, want.Exe)
	}
	if want.Cmd != "" && info.Cmd != want.Cmd {
		t.Fatalf("cmd = %q, want %q", info.Cmd, want.Cmd)
	}
	if want.User != "" && info.User != want.User {
		t.Fatalf("user = %q, want %q", info.User, want.User)
	}
}

func TestSuffixVarPicksSortedFirstOnMultipleMatches(t *testing.T) {
	// Several variables share the suffix; the result must be the alphabetically
	// first key's value, deterministically, regardless of map ordering.
	vars := map[string]string{
		"ZEBRA_PIDFILE":  "/run/zebra.pid",
		"ALPHA_PIDFILE":  "/run/alpha.pid",
		"MIDDLE_PIDFILE": "/run/middle.pid",
		"OTHER":          "ignored",
	}
	for range 20 {
		if got := suffixVar(vars, "_PIDFILE"); got != "/run/alpha.pid" {
			t.Fatalf("suffixVar = %q, want /run/alpha.pid (deterministic sorted pick)", got)
		}
	}
}

func TestResolveOpenRCValueDefault(t *testing.T) {
	vars := map[string]string{"PIDFILE": "/run/x.pid", "EMPTY": ""}
	// ${VAR:-default}: a set, non-empty var uses its own value...
	if got, ok := resolveOpenRCValue("${PIDFILE:-/run/d.pid}", vars); !ok || got != "/run/x.pid" {
		t.Fatalf("set var = (%q,%v), want /run/x.pid", got, ok)
	}
	// ...an empty (or unset) var falls back to the default.
	if got, ok := resolveOpenRCValue("${EMPTY:-/run/d.pid}", vars); !ok || got != "/run/d.pid" {
		t.Fatalf("empty var = (%q,%v), want /run/d.pid", got, ok)
	}
}

func TestTrimShellPrefixPattern(t *testing.T) {
	// A wildcard prefix match at index 0 still strips through the suffix.
	if got := trimShellPrefixPattern("foobar", "*foo"); got != "bar" {
		t.Errorf("trimShellPrefixPattern(foobar, *foo) = %q, want bar", got)
	}
	// A literal prefix is trimmed.
	if got := trimShellPrefixPattern("/usr/bin", "/usr"); got != "/bin" {
		t.Errorf("trimShellPrefixPattern(/usr/bin, /usr) = %q, want /bin", got)
	}
}

func TestDefaultExprNestedDepth(t *testing.T) {
	// A ":-" inside a nested ${...} must be skipped; only the outer one (at brace
	// depth 0) splits name from default.
	name, def, ok := defaultExpr("${${A:-b}c:-d}")
	if !ok || name != "${A:-b}c" || def != "d" {
		t.Fatalf("defaultExpr nested = (%q,%q,%v), want ${A:-b}c / d / true", name, def, ok)
	}
	// No separator: scans the whole body (without reading past its end) and yields
	// no default.
	if _, _, ok := defaultExpr("${ABC}"); ok {
		t.Fatalf("defaultExpr(${ABC}) ok = %v, want false", ok)
	}
}

func TestOpenRCAssignmentsIfElseFi(t *testing.T) {
	// X empty -> if-condition false -> the else branch runs.
	elseVars := openRCAssignments("X=\nif [ -n \"${X}\" ]; then\nINSIDE=a\nelse\nELSEVAL=b\nfi\n", "svc")
	if elseVars["ELSEVAL"] != "b" {
		t.Fatalf("else branch must run, ELSEVAL=%q", elseVars["ELSEVAL"])
	}
	if _, set := elseVars["INSIDE"]; set {
		t.Fatalf("if-body must be skipped, INSIDE=%q", elseVars["INSIDE"])
	}
	// After a false (no-else) if block, `fi` restores the active state so trailing
	// lines run again.
	fiVars := openRCAssignments("X=\nif [ -n \"${X}\" ]; then\nINSIDE=a\nfi\nAFTER=c\n", "svc")
	if _, set := fiVars["INSIDE"]; set {
		t.Fatalf("if-body must be skipped, INSIDE=%q", fiVars["INSIDE"])
	}
	if fiVars["AFTER"] != "c" {
		t.Fatalf("AFTER = %q, want c (active restored after fi)", fiVars["AFTER"])
	}
}

func TestOpenRCAssignmentsNestedBranches(t *testing.T) {
	vars := openRCAssignments(`
OUTER=yes
MISSING=
if [ -n "${OUTER}" ]; then
  BEFORE=ok
  if [ -n "${MISSING}" ]; then
    WRONG=inner
  else
    INNER=ok
  fi
else
  WRONG=outer
fi
AFTER=ok
`, "svc")
	for name, want := range map[string]string{"BEFORE": "ok", "INNER": "ok", "AFTER": "ok"} {
		if got := vars[name]; got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	if got := vars["WRONG"]; got != "" {
		t.Fatalf("inactive branch assigned WRONG=%q", got)
	}
}
