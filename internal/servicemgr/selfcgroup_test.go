package servicemgr

import (
	"errors"
	"testing"
)

func cgroupFiles(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		data, ok := files[path]
		if !ok {
			return nil, errors.New("no such file: " + path)
		}
		return []byte(data), nil
	}
}

func TestSelfUnitCgroupPIDsReadsServiceUnit(t *testing.T) {
	readFile := cgroupFiles(map[string]string{
		selfCgroupPath: "0::/system.slice/sermod.service\n",
		cgroupRoot + "/system.slice/sermod.service/cgroup.procs": "4242\n4243\n\n4244\n",
	})
	got, unitName, found := SelfUnitCgroupPIDs(readFile)
	if !found {
		t.Fatal("a systemd service unit cgroup must be readable")
	}
	if unitName != "sermod.service" {
		t.Fatalf("unit = %q, want sermod.service", unitName)
	}
	want := []int{4242, 4243, 4244}
	if len(got) != len(want) {
		t.Fatalf("pids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pids = %v, want %v", got, want)
		}
	}
}

// The refusals matter more than the success: outside a service unit the cgroup is
// shared with processes that are not ours, and treating those as leftovers would
// terminate the operator's own shell.
func TestSelfUnitCgroupPIDsRefusesNonServiceCgroups(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{
			name: "login session scope",
			files: map[string]string{
				selfCgroupPath: "0::/user.slice/user-0.slice/session-7.scope\n",
				cgroupRoot + "/user.slice/user-0.slice/session-7.scope/cgroup.procs": "10\n11\n",
			},
		},
		{
			name: "bare slice",
			files: map[string]string{
				selfCgroupPath: "0::/system.slice\n",
				cgroupRoot + "/system.slice/cgroup.procs": "10\n",
			},
		},
		{
			name:  "cgroup root",
			files: map[string]string{selfCgroupPath: "0::/\n"},
		},
		{
			name: "cgroup v1 hierarchy",
			files: map[string]string{
				selfCgroupPath: "12:pids:/system.slice/sermod.service\n7:memory:/system.slice/sermod.service\n",
			},
		},
		{
			name:  "unreadable cgroup",
			files: map[string]string{},
		},
		{
			name:  "unreadable process list",
			files: map[string]string{selfCgroupPath: "0::/system.slice/sermod.service\n"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := SelfUnitCgroupPIDs(cgroupFiles(tc.files)); ok {
				t.Fatal("must refuse to report a cgroup it does not exclusively own")
			}
		})
	}
}
