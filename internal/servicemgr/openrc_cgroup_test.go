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
	pids := openRCPIDs(t, BackendPIDsFuncWithRunner(context.Background(), BackendOpenRC, "sshd", nil, read))
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
// The pidfile is read the way discovery reads it: PostgreSQL's postmaster.pid
// keeps the PID on line one and cluster metadata below, and a process-group
// pidfile names its leader as a negative number. Either must seed the cgroup.
func TestOpenRCCgroupReadsPidfileLikeDiscovery(t *testing.T) {
	for name, content := range map[string]string{
		"postmaster.pid": "90\n/var/lib/postgresql/16/main\n1729000000\n5432\n",
		"process group":  "-90\n",
	} {
		t.Run(name, func(t *testing.T) {
			pidfile := filepath.Join(t.TempDir(), "postgresql.pid")
			writePidfile(t, pidfile, content)
			files := map[string]string{
				"/etc/init.d/postgresql":                        "command=/usr/bin/postgres\npidfile=" + pidfile + "\n",
				"/sys/fs/cgroup/openrc.postgresql/cgroup.procs": "90\n91\n",
			}
			pids := openRCPIDs(t, BackendPIDsFuncWithRunner(context.Background(), BackendOpenRC, "postgresql", nil, cgroupFiles(files)))
			if got := pids(); !reflect.DeepEqual(got, []int{90, 91}) {
				t.Fatalf("pids = %v, want the principal and its cgroup", got)
			}
		})
	}
}

func TestOpenRCCgroupIgnoresFIFOPidfile(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "sshd.pid")
	if err := syscall.Mkfifo(pidfile, 0o600); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"/etc/init.d/sshd":                        "command=/usr/bin/sshd\npidfile=" + pidfile + "\n",
		"/sys/fs/cgroup/openrc.sshd/cgroup.procs": "90\n",
	}
	pids := openRCPIDs(t, BackendPIDsFuncWithRunner(context.Background(), BackendOpenRC, "sshd", nil, cgroupFiles(files)))
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

// openRCPIDs reads an OpenRC query, which has no query of its own to fail.
func openRCPIDs(t *testing.T, query func() ([]int, error)) func() []int {
	t.Helper()
	return func() []int {
		pids, err := query()
		if err != nil {
			t.Errorf("OpenRC backend PIDs error = %v", err)
		}
		return pids
	}
}
