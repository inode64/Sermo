package servicemgr

import (
	"context"
	"path/filepath"
	"testing"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
)

func TestCgroupReadersRejectUnsafePathsBeforeRead(t *testing.T) {
	for _, group := range []string{
		"", "/", "system.slice/x.service", "../x.service", "/../../tmp/x.service",
		"/system.slice/../x.service", "/system.slice/./x.service", "//system.slice/x.service",
		"/system.slice/x\x00.service",
	} {
		t.Run(group, func(t *testing.T) {
			read := func(path string) ([]byte, error) {
				t.Errorf("unsafe cgroup %q read %s", group, path)
				return []byte("42\n"), nil
			}
			runner := execxtest.Fixed(execx.Result{Stdout: group}, nil)
			if pids, ok := CgroupPIDsContext(context.Background(), runner, read, BackendSystemd, "x.service"); ok || len(pids) != 0 {
				t.Fatalf("unsafe cgroup returned PIDs: %v, %v", pids, ok)
			}
			selfRead := func(path string) ([]byte, error) {
				if path == selfCgroupPath {
					return []byte("0::" + group + "\n"), nil
				}
				return read(path)
			}
			if pids, unit, ok := SelfUnitCgroupPIDs(selfRead); ok || unit != "" || len(pids) != 0 {
				t.Fatalf("unsafe self cgroup returned ownership: %v, %q, %v", pids, unit, ok)
			}
		})
	}
}

func TestOpenRCReadersRejectUnsafeUnitsBeforeRead(t *testing.T) {
	for _, unit := range []string{"", ".", "..", "../victim", "/etc/victim", "a/b", "a/../victim", `a\b`, "a\x00b"} {
		t.Run(unit, func(t *testing.T) {
			read := func(path string) ([]byte, error) {
				t.Errorf("unsafe OpenRC unit %q read %s", unit, path)
				return []byte("command=/usr/bin/victim\nreload() {}\n"), nil
			}
			if got := DetectProcInfo(context.Background(), nil, read, BackendOpenRC, unit); got != (ProcInfo{}) {
				t.Fatalf("unsafe unit produced process selectors: %+v", got)
			}
			if got := detectOpenRCRuntimeProc(read, unit); got != (ProcInfo{}) {
				t.Fatalf("unsafe unit produced runtime selectors: %+v", got)
			}
			manager := openrcManager{readFile: read}
			if supported, err := manager.SupportsReload(context.Background(), unit); supported || err == nil {
				t.Fatalf("unsafe reload query = %v, %v; want false and error", supported, err)
			}
			r := resolver(nil, map[string]bool{filepath.Join(openRCInitDir, unit): true})
			for _, trust := range []bool{false, true} {
				if got, err := r.Resolve(context.Background(), BackendOpenRC, []string{unit}, trust); got != "" || err == nil {
					t.Fatalf("unsafe unit resolved with trust=%v: %q, %v", trust, got, err)
				}
			}
			if r.knows(context.Background(), BackendOpenRC, unit, unit, nil, r.Probe) {
				t.Fatal("unsafe unit recognized as an init script")
			}
		})
	}
}
