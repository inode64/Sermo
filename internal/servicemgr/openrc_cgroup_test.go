package servicemgr

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

func TestOpenRCCgroupPrincipalAndResiduals(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "sshd.pid")
	writePidfile(t, pidfile, "90\n")
	files := map[string]string{
		"/etc/init.d/sshd":                        "command=/usr/bin/sshd\npidfile=" + pidfile + "\n",
		"/sys/fs/cgroup/openrc.sshd/cgroup.procs": "301\n302\n90\n90\n1\ninvalid\n",
	}
	read := cgroupFiles(files)
	pids := BackendPIDsFuncWithRunner(context.Background(), BackendOpenRC, "sshd", nil, read)
	if got := pids(); !reflect.DeepEqual(got, []int{90, 301, 302}) {
		t.Fatalf("pids=%v", got)
	}
	writePidfile(t, pidfile, "301\n")
	if got := pids(); !reflect.DeepEqual(got, []int{301, 302, 90}) {
		t.Fatalf("new principal pids=%v", got)
	}
	for _, invalid := range []string{"", "1", "99", "bad"} {
		writePidfile(t, pidfile, invalid)
		if got := pids(); len(got) != 0 {
			t.Fatalf("pidfile=%q pids=%v", invalid, got)
		}
	}
	writePidfile(t, pidfile, "90")
	delete(files, "/sys/fs/cgroup/openrc.sshd/cgroup.procs")
	if got := pids(); len(got) != 0 {
		t.Fatalf("missing cgroup pids=%v", got)
	}
}

// A FIFO at the init-declared pidfile path must not block the query, which
// runs under the cached backend PID lock every discoverer of the unit shares.
func TestOpenRCCgroupIgnoresFIFOPidfile(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "sshd.pid")
	if err := syscall.Mkfifo(pidfile, 0o600); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"/etc/init.d/sshd":                        "command=/usr/bin/sshd\npidfile=" + pidfile + "\n",
		"/sys/fs/cgroup/openrc.sshd/cgroup.procs": "90\n",
	}
	pids := BackendPIDsFuncWithRunner(context.Background(), BackendOpenRC, "sshd", nil, cgroupFiles(files))
	done := make(chan []int, 1)
	go func() { done <- pids() }()
	select {
	case got := <-done:
		if len(got) != 0 {
			t.Fatalf("FIFO pidfile pids=%v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO pidfile blocked the backend PID query")
	}
}

func writePidfile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRCCgroupRejectsUnsafeUnit(t *testing.T) {
	for _, unit := range []string{"", ".", "..", "../sshd", "/sshd", "a/b", "a\\b", "a\x00b"} {
		pids := openRCBackendPIDs(unit, func(path string) ([]byte, error) { t.Fatalf("unsafe unit %q read %s", unit, path); return nil, nil })
		if got := pids(); len(got) != 0 {
			t.Fatalf("pids=%v", got)
		}
	}
}
