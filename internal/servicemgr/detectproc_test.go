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

func TestDetectProcOpenRCSkipsVariablePidfile(t *testing.T) {
	// A pidfile built from a variable is not a literal path; it must be skipped
	// rather than emitting a useless `pidfile: ${...}`.
	read := func(path string) ([]byte, error) {
		if path == "/etc/init.d/bar" {
			return []byte("pidfile=\"${RC_PREFIX}/run/bar.pid\"\ncommand=/usr/bin/bar\n"), nil
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
