package servicemgr

import (
	"context"
	"testing"

	"sermo/internal/execx"
)

func TestDetectProcSystemd(t *testing.T) {
	runner := fakeRunner{results: map[string]execx.Result{
		"systemctl show -p PIDFile --value -- nginx.service":   {Stdout: "/run/nginx.pid\n"},
		"systemctl show -p ExecStart --value -- nginx.service": {Stdout: "{ path=/usr/sbin/nginx ; argv[]=/usr/sbin/nginx -g daemon off ; ignore_errors=no }\n"},
	}}
	pidfile, exe := DetectProc(context.Background(), runner, nil, BackendSystemd, "nginx.service")
	if pidfile != "/run/nginx.pid" {
		t.Fatalf("pidfile = %q, want /run/nginx.pid", pidfile)
	}
	if exe != "/usr/sbin/nginx" {
		t.Fatalf("exe = %q, want /usr/sbin/nginx", exe)
	}
}

func TestDetectProcSystemdExecStartOnly(t *testing.T) {
	// No PIDFile= in the unit: pidfile is empty, but the exe is still derived.
	runner := fakeRunner{results: map[string]execx.Result{
		"systemctl show -p ExecStart --value -- sshd.service": {Stdout: "{ path=/usr/sbin/sshd ; argv[]=/usr/sbin/sshd -D }\n"},
	}}
	pidfile, exe := DetectProc(context.Background(), runner, nil, BackendSystemd, "sshd.service")
	if pidfile != "" {
		t.Fatalf("pidfile = %q, want empty", pidfile)
	}
	if exe != "/usr/sbin/sshd" {
		t.Fatalf("exe = %q, want /usr/sbin/sshd", exe)
	}
}

func TestDetectProcSystemdNormalizesLegacyVarRun(t *testing.T) {
	runner := fakeRunner{results: map[string]execx.Result{
		"systemctl show -p PIDFile --value -- apache.service":   {Stdout: "/var/run/apache2.pid\n"},
		"systemctl show -p ExecStart --value -- apache.service": {Stdout: "{ path=/usr/sbin/apache2 ; argv[]=/usr/sbin/apache2 -k start ; ignore_errors=no }\n"},
	}}
	pidfile, exe := DetectProc(context.Background(), runner, nil, BackendSystemd, "apache.service")
	if pidfile != "/run/apache2.pid" {
		t.Fatalf("pidfile = %q, want /run/apache2.pid", pidfile)
	}
	if exe != "/usr/sbin/apache2" {
		t.Fatalf("exe = %q, want /usr/sbin/apache2", exe)
	}
}

func TestDetectProcOpenRCPidfile(t *testing.T) {
	read := func(path string) ([]byte, error) {
		switch path {
		case "/etc/init.d/nginx":
			return []byte("#!/sbin/openrc-run\ncommand=\"/usr/sbin/nginx\"\npidfile=\"/run/nginx.pid\"\n"), nil
		}
		return nil, errNotFound
	}
	pidfile, exe := DetectProc(context.Background(), nil, read, BackendOpenRC, "nginx")
	if pidfile != "/run/nginx.pid" {
		t.Fatalf("pidfile = %q, want /run/nginx.pid", pidfile)
	}
	if exe != "/usr/sbin/nginx" {
		t.Fatalf("exe = %q, want /usr/sbin/nginx", exe)
	}
}

func TestDetectProcOpenRCStartStopDaemonArg(t *testing.T) {
	// pidfile passed as a start-stop-daemon argument, command in conf.d.
	read := func(path string) ([]byte, error) {
		switch path {
		case "/etc/init.d/foo":
			return []byte("start() {\n  start-stop-daemon --start --pidfile /run/foo/foo.pid --exec /usr/bin/foo\n}\n"), nil
		}
		return nil, errNotFound
	}
	pidfile, _ := DetectProc(context.Background(), nil, read, BackendOpenRC, "foo")
	if pidfile != "/run/foo/foo.pid" {
		t.Fatalf("pidfile = %q, want /run/foo/foo.pid", pidfile)
	}
}

func TestDetectProcOpenRCCleansAbsolutePaths(t *testing.T) {
	read := func(path string) ([]byte, error) {
		switch path {
		case "/etc/init.d/dhcpd":
			return []byte(`pidfile="//run/dhcp/dhcpd.pid"
command="//usr/sbin/dhcpd"
`), nil
		}
		return nil, errNotFound
	}
	info := DetectProcInfo(context.Background(), nil, read, BackendOpenRC, "dhcpd")
	if info.Pidfile != "/run/dhcp/dhcpd.pid" {
		t.Fatalf("pidfile = %q, want /run/dhcp/dhcpd.pid", info.Pidfile)
	}
	if info.Exe != "/usr/sbin/dhcpd" {
		t.Fatalf("exe = %q, want /usr/sbin/dhcpd", info.Exe)
	}
	if info.Cmd != `(^|[[:space:]])/usr/sbin/dhcpd($|[[:space:]])` {
		t.Fatalf("cmd = %q", info.Cmd)
	}
}

func TestDetectProcNormalizesLegacyVarRun(t *testing.T) {
	read := func(path string) ([]byte, error) {
		switch path {
		case "/etc/init.d/apache2":
			return []byte(`pidfile="/var/run/apache2.pid"
command="/usr/sbin/apache2"
`), nil
		}
		return nil, errNotFound
	}
	info := DetectProcInfo(context.Background(), nil, read, BackendOpenRC, "apache2")
	if info.Pidfile != "/run/apache2.pid" {
		t.Fatalf("pidfile = %q, want /run/apache2.pid", info.Pidfile)
	}
}

func TestDetectProcOpenRCSkipsNonAbsoluteExec(t *testing.T) {
	read := func(path string) ([]byte, error) {
		switch path {
		case "/etc/init.d/net.eth0":
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
	read := func(path string) ([]byte, error) {
		switch path {
		case "/etc/init.d/apache2":
			return []byte(`PIDFILE="${PIDFILE:-/run/apache2.pid}"
APACHE2="/usr/sbin/apache2"
start() {
	start-stop-daemon --start --pidfile "${PIDFILE}" -- \
		${APACHE2} ${APACHE2_OPTS} -k start
}
`), nil
		}
		return nil, errNotFound
	}
	info := DetectProcInfo(context.Background(), nil, read, BackendOpenRC, "apache2")
	if info.Pidfile != "/run/apache2.pid" {
		t.Fatalf("pidfile = %q, want /run/apache2.pid", info.Pidfile)
	}
	if info.Exe != "/usr/sbin/apache2" {
		t.Fatalf("exe = %q, want /usr/sbin/apache2", info.Exe)
	}
}

func TestDetectProcOpenRCExpandsServiceNameAndEmptyRoots(t *testing.T) {
	read := func(path string) ([]byte, error) {
		switch path {
		case "/etc/init.d/sshd":
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
	read := func(path string) ([]byte, error) {
		switch path {
		case "/etc/init.d/php8.2":
			return []byte(`PHP_SLOT="${SVCNAME#php-fpm-}"
command="/usr/lib64/${PHP_SLOT}/bin/php-fpm"
pidfile="/run/php-fpm-${PHP_SLOT}.pid"
`), nil
		}
		return nil, errNotFound
	}
	info := DetectProcInfo(context.Background(), nil, read, BackendOpenRC, "php8.2")
	if info.Pidfile != "/run/php-fpm-php8.2.pid" {
		t.Fatalf("pidfile = %q, want /run/php-fpm-php8.2.pid", info.Pidfile)
	}
	if info.Exe != "/usr/lib64/php8.2/bin/php-fpm" {
		t.Fatalf("exe = %q, want /usr/lib64/php8.2/bin/php-fpm", info.Exe)
	}
}

func TestDetectProcOpenRCPatternPrefixRemoval(t *testing.T) {
	read := func(path string) ([]byte, error) {
		switch path {
		case "/etc/init.d/openvpn.tun1":
			return []byte(`VPN=${SVCNAME#*.}
if [ -n "${VPN}" ] && [ ${SVCNAME} != "openvpn" ]; then
	VPNPID="/run/openvpn.${VPN}.pid"
else
	VPNPID="/run/openvpn.pid"
fi
start-stop-daemon --start --exec /usr/sbin/openvpn -- \
	--config "/etc/openvpn/${VPN}.conf" --writepid "${VPNPID}" --daemon
`), nil
		}
		return nil, errNotFound
	}
	info := DetectProcInfo(context.Background(), nil, read, BackendOpenRC, "openvpn.tun1")
	if info.Pidfile != "/run/openvpn.tun1.pid" {
		t.Fatalf("pidfile = %q, want /run/openvpn.tun1.pid", info.Pidfile)
	}
	if info.Exe != "/usr/sbin/openvpn" {
		t.Fatalf("exe = %q, want /usr/sbin/openvpn", info.Exe)
	}
}

func TestDetectProcOpenRCChrootDefaultsToHostRoot(t *testing.T) {
	read := func(path string) ([]byte, error) {
		switch path {
		case "/etc/init.d/named":
			return []byte(`PIDFILE="${CHROOT}/run/named/named.pid"
start-stop-daemon --start --pidfile ${PIDFILE} --exec /usr/sbin/named
`), nil
		}
		return nil, errNotFound
	}
	info := DetectProcInfo(context.Background(), nil, read, BackendOpenRC, "named")
	if info.Pidfile != "/run/named/named.pid" {
		t.Fatalf("pidfile = %q, want /run/named/named.pid", info.Pidfile)
	}
	if info.Exe != "/usr/sbin/named" {
		t.Fatalf("exe = %q, want /usr/sbin/named", info.Exe)
	}
}

func TestDetectProcOpenRCCommandUser(t *testing.T) {
	read := func(path string) ([]byte, error) {
		switch path {
		case "/etc/init.d/influxdb":
			return []byte(`user=${user:-influxdb}
group=${group:-influxdb}
command=/usr/bin/influxd
command_user="${user}:${group}"
`), nil
		}
		return nil, errNotFound
	}
	info := DetectProcInfo(context.Background(), nil, read, BackendOpenRC, "influxdb")
	if info.Exe != "/usr/bin/influxd" {
		t.Fatalf("exe = %q, want /usr/bin/influxd", info.Exe)
	}
	if info.User != "influxdb" {
		t.Fatalf("user = %q, want influxdb", info.User)
	}
	if info.Cmd != `(^|[[:space:]])/usr/bin/influxd($|[[:space:]])` {
		t.Fatalf("cmd = %q", info.Cmd)
	}
}

func TestDetectProcOpenRCRuntimeOptionsFallback(t *testing.T) {
	read := func(path string) ([]byte, error) {
		switch path {
		case "/etc/init.d/mysql":
			return []byte(`MY_CNF="${MY_CNF:-/etc/${SVCNAME}/my.cnf}"
start() {
	pidfile=$(get_config "${MY_CNF}" 'pid[_-]file' | tail -n1)
	start-stop-daemon --start --exec /usr/sbin/mysqld --pidfile "${pidfile}"
	save_options pidfile "${pidfile}"
}
`), nil
		case "/run/openrc/daemons/mysql/001":
			return []byte(`exec=/usr/sbin/mysqld
argv_0=/usr/sbin/mysqld
argv_1=--defaults-file=/etc/mysql/my.cnf
pidfile=/run/mysqld/mariadb.pid
`), nil
		}
		return nil, errNotFound
	}
	info := DetectProcInfo(context.Background(), nil, read, BackendOpenRC, "mysql")
	if info.Pidfile != "/run/mysqld/mariadb.pid" {
		t.Fatalf("pidfile = %q, want /run/mysqld/mariadb.pid", info.Pidfile)
	}
	if info.Exe != "/usr/sbin/mysqld" {
		t.Fatalf("exe = %q, want /usr/sbin/mysqld", info.Exe)
	}
	if info.Cmd != `(^|[[:space:]])/usr/sbin/mysqld($|[[:space:]])` {
		t.Fatalf("cmd = %q", info.Cmd)
	}
}

func TestDetectProcOpenRCSkipsVariablePidfile(t *testing.T) {
	// A pidfile built from a variable is not a literal path; it must be skipped
	// rather than emitting a useless `pidfile: ${...}`.
	read := func(path string) ([]byte, error) {
		if path == "/etc/init.d/bar" {
			return []byte("pidfile=\"${RUNTIME_DIR}/bar.pid\"\ncommand=/usr/bin/bar\n"), nil
		}
		return nil, errNotFound
	}
	pidfile, exe := DetectProc(context.Background(), nil, read, BackendOpenRC, "bar")
	if pidfile != "" {
		t.Fatalf("pidfile = %q, want empty (variable, not literal)", pidfile)
	}
	if exe != "/usr/bin/bar" {
		t.Fatalf("exe = %q, want /usr/bin/bar", exe)
	}
}

var errNotFound = &fakeFSError{}

type fakeFSError struct{}

func (*fakeFSError) Error() string { return "not found" }

func TestSuffixVarPicksSortedFirstOnMultipleMatches(t *testing.T) {
	// Several variables share the suffix; the result must be the alphabetically
	// first key's value, deterministically, regardless of map ordering.
	vars := map[string]string{
		"ZEBRA_PIDFILE":  "/run/zebra.pid",
		"ALPHA_PIDFILE":  "/run/alpha.pid",
		"MIDDLE_PIDFILE": "/run/middle.pid",
		"OTHER":          "ignored",
	}
	for i := 0; i < 20; i++ {
		if got := suffixVar(vars, "_PIDFILE"); got != "/run/alpha.pid" {
			t.Fatalf("suffixVar = %q, want /run/alpha.pid (deterministic sorted pick)", got)
		}
	}
}
