package app

import (
	"testing"

	"sermo/internal/execx/execxtest"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

type serviceOwnershipReader map[int]process.Identity

func (r serviceOwnershipReader) PIDs() ([]int, error) {
	pids := make([]int, 0, len(r))
	for pid := range r {
		pids = append(pids, pid)
	}
	return pids, nil
}

func (r serviceOwnershipReader) Identity(pid int) (process.Identity, bool) {
	id, ok := r[pid]
	return id, ok
}

func TestServiceProcessOwnershipRespectsBackendNamespace(t *testing.T) {
	const exe = "/opt/sermo-test/worker"
	for _, tc := range []struct {
		name    string
		backend servicemgr.Backend
		cgroup  string
		want    int
	}{
		{name: "systemd own unit", backend: servicemgr.BackendSystemd, cgroup: "0::/system.slice/svc.service", want: 1},
		{name: "systemd foreign unit", backend: servicemgr.BackendSystemd, cgroup: "0::/system.slice/other.service"},
		{name: "openrc own unit", backend: servicemgr.BackendOpenRC, cgroup: "0::/openrc.svc", want: 1},
		{name: "openrc foreign unit", backend: servicemgr.BackendOpenRC, cgroup: "0::/openrc.other"},
		{name: "libvirt domain scope", backend: servicemgr.BackendLibvirt, cgroup: "0::/machine.slice/machine-qemu-1-svc.scope", want: 1},
		{name: "libvirt network daemon", backend: servicemgr.BackendLibvirtNetwork, cgroup: "0::/system.slice/virtnetworkd.service", want: 1},
		{name: "docker scope", backend: servicemgr.BackendDocker, cgroup: "0::/system.slice/docker-container.scope", want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := serviceOwnershipReader{100: {PID: 100, PPID: 1, UID: 1000, Exe: exe, ExeOK: true, Cgroup: tc.cgroup}}
			runtime := BuildServiceRuntime(t.Context(), ServiceRuntimeConfig{
				Service: "svc", Unit: "svc", Tree: map[string]any{
					"processes": map[string]any{"main": map[string]any{"exe": exe, "user": "worker"}},
				},
				Deps: Deps{Backend: tc.backend, Manager: fakeManager{}, Runtime: t.TempDir(), ProcReader: reader, ExecxRunner: &execxtest.Runner{}},
			})
			runtime.Discoverer.ResolveUser = func(string) (uint32, bool) { return 1000, true }
			procs, warnings := runtime.Discoverer.Discover(runtime.Selectors)
			if len(warnings) != 0 || len(procs) != tc.want {
				t.Fatalf("Discover() = %+v, warnings %v; want %d processes", procs, warnings, tc.want)
			}
			if tc.backend != servicemgr.BackendLibvirt {
				return
			}
			id := reader[100]
			id.UID = 1001
			reader[100] = id
			if procs, _ := runtime.Discoverer.Discover(runtime.Selectors); len(procs) != 0 {
				t.Fatal("control target accepted the wrong process user")
			}
			id.UID, id.Exe, id.ExeOK, id.ExePrev = 1000, "", false, exe
			reader[100] = id
			observation, err := runtime.Discoverer.Observe(runtime.Selectors)
			if err != nil || len(observation.Processes) != 1 || observation.Processes[0].SignalBlockReason == "" {
				t.Fatalf("unattributed deleted control process must block signaling: %+v, %v", observation, err)
			}
		})
	}
}
