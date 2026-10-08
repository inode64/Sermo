package servicemgr

import (
	"errors"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
)

func TestClassifyCgroup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, content string
		want          CgroupOwner
	}{
		{"service", "0::/system.slice/nginx.service", CgroupOwner{Class: CgroupClassService, Owner: "nginx"}},
		{"nested service", "0::/system.slice/system-getty.slice/getty@tty1.service", CgroupOwner{Class: CgroupClassService, Owner: "getty@tty1"}},
		{"service worker subgroup", "0::/system.slice/squid.service/worker", CgroupOwner{Class: CgroupClassService, Owner: "squid"}},
		{"docker scope", "0::/system.slice/docker-abc.scope", CgroupOwner{Class: CgroupClassScope, Owner: "docker-abc"}},
		{"machine scope", "0::/machine.slice/machine-qemu\\x2d1\\x2dvm.scope", CgroupOwner{Class: CgroupClassScope, Owner: "machine-qemu\\x2d1\\x2dvm"}},
		{"init", "0::/init.scope", CgroupOwner{Class: CgroupClassInit}},
		{"session", "0::/user.slice/user-0.slice/session-23.scope", CgroupOwner{Class: CgroupClassSession, Owner: "23"}},
		{"user manager", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/foo.service", CgroupOwner{Class: CgroupClassUserManager, Owner: "1000"}},
		{"user slice only", "0::/user.slice/user-1000.slice", CgroupOwner{Class: CgroupClassSlice}},
		{"user transient scope", "0::/user.slice/user-1000.slice/run-u5.scope", CgroupOwner{Class: CgroupClassScope, Owner: "run-u5"}},
		{"system slice only", "0::/system.slice", CgroupOwner{Class: CgroupClassSlice}},
		{"openrc", "0::/openrc.sshd", CgroupOwner{Class: CgroupClassService, Owner: "sshd"}},
		{"root", "0::/", CgroupOwner{Class: CgroupClassRoot}},
		{"v1 named", "3:cpu:/\n1:name=systemd:/system.slice/cron.service", CgroupOwner{Class: CgroupClassService, Owner: "cron"}},
		{"v1 root", "1:name=systemd:/", CgroupOwner{Class: CgroupClassRoot}},
		{"v1 without systemd", "3:cpu:/\n2:memory:/", CgroupOwner{Class: CgroupClassUnknown}},
		{"malformed", "0::/system.slice/../nginx.service", CgroupOwner{Class: CgroupClassUnknown}},
		{"relative", "0::nginx.service", CgroupOwner{Class: CgroupClassUnknown}},
		{"empty", "", CgroupOwner{Class: CgroupClassUnknown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ClassifyCgroup(tc.content); got != tc.want {
				t.Fatalf("ClassifyCgroup(%q) = %+v, want %+v", tc.content, got, tc.want)
			}
		})
	}
}

func TestProcfsCgroupPathPrefersUnifiedRecord(t *testing.T) {
	t.Parallel()
	path, ok := ProcfsCgroupPath("1:name=systemd:/old.service\n0::/system.slice/new.service")
	if !ok || path != "/old.service" {
		t.Fatalf("first matching record wins: got %q ok=%v", path, ok)
	}
	if _, ok := ProcfsCgroupPath("  \n"); ok {
		t.Fatal("blank content must not resolve")
	}
}

func TestOpenRCUnitCgroupsPresent(t *testing.T) {
	t.Parallel()
	readDirFrom := func(fsys fs.FS) func(string) ([]os.DirEntry, error) {
		return func(string) ([]os.DirEntry, error) { return fs.ReadDir(fsys, ".") }
	}
	withUnit := fstest.MapFS{"openrc.sshd/cgroup.procs": {Data: []byte("1\n")}, "cpu.max": {Data: []byte("max")}}
	if !OpenRCUnitCgroupsPresent(readDirFrom(withUnit)) {
		t.Fatal("an openrc.<service> directory must count as a unit group")
	}
	noUnit := fstest.MapFS{"openrc.sshd": {Data: []byte("not a dir")}, "system.slice/x": {Data: nil}}
	if OpenRCUnitCgroupsPresent(readDirFrom(noUnit)) {
		t.Fatal("a file named openrc.* or systemd slices must not count")
	}
	if OpenRCUnitCgroupsPresent(func(string) ([]os.DirEntry, error) { return nil, errors.New("boom") }) {
		t.Fatal("an unreadable cgroup root must answer false")
	}
}
