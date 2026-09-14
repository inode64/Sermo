package servicemgr

import (
	"context"
	"reflect"
	"testing"
)

func TestOpenRCCgroupPrincipalAndResiduals(t *testing.T) {
	files := map[string]string{
		"/etc/init.d/sshd": "command=/usr/bin/sshd\npidfile=/run/sshd.pid\n",
		"/run/sshd.pid":    "90\n",
		"/sys/fs/cgroup/openrc.sshd/cgroup.procs": "301\n302\n90\n90\n1\ninvalid\n",
	}
	read := cgroupFiles(files)
	pids := BackendPIDsFuncWithRunner(context.Background(), BackendOpenRC, "sshd", nil, read)
	if got := pids(); !reflect.DeepEqual(got, []int{90, 301, 302}) {
		t.Fatalf("pids=%v", got)
	}
	files["/run/sshd.pid"] = "301\n"
	if got := pids(); !reflect.DeepEqual(got, []int{301, 302, 90}) {
		t.Fatalf("new principal pids=%v", got)
	}
	for _, invalid := range []string{"", "1", "99", "bad"} {
		files["/run/sshd.pid"] = invalid
		if got := pids(); len(got) != 0 {
			t.Fatalf("pidfile=%q pids=%v", invalid, got)
		}
	}
	files["/run/sshd.pid"] = "90"
	delete(files, "/sys/fs/cgroup/openrc.sshd/cgroup.procs")
	if got := pids(); len(got) != 0 {
		t.Fatalf("missing cgroup pids=%v", got)
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
