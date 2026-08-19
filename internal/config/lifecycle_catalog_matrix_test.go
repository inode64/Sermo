package config

import (
	"slices"
	"testing"
)

func TestRealCatalogLifecycleBackendMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		service           string
		backend           string
		processMode       ServiceProcessMode
		restartMode       RestartMode
		auxiliaryUnits    []string
		allowDependencies bool
		socketActivated   bool
		delegatedRoles    []string
	}{
		{name: "docker systemd", service: "docker", backend: backendSystemd, processMode: ServiceProcessResident, restartMode: RestartModeNative, auxiliaryUnits: []string{"docker.socket"}, socketActivated: true, delegatedRoles: []string{"proxy"}},
		{name: "docker openrc", service: "docker", backend: backendOpenRC, processMode: ServiceProcessResident, restartMode: RestartModeNative, delegatedRoles: []string{"proxy"}},
		{name: "nfs systemd", service: "nfs", backend: backendSystemd, processMode: ServiceProcessNone, restartMode: RestartModeStaged, allowDependencies: true},
		{name: "nfs openrc", service: "nfs", backend: backendOpenRC, processMode: ServiceProcessNone, restartMode: RestartModeStaged, allowDependencies: true},
		{name: "lvm monitor systemd", service: "lvm2-monitor", backend: backendSystemd, processMode: ServiceProcessNone, restartMode: RestartModeStaged},
		{name: "cockpit socket systemd", service: "cockpit", backend: backendSystemd, processMode: ServiceProcessInit, restartMode: RestartModeStaged, socketActivated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolved := resolveCatalogService(t, tt.service, tt.backend)
			lifecycle, err := ResolveServiceLifecycle(resolved.Tree, tt.backend)
			if err != nil {
				t.Fatalf("ResolveServiceLifecycle: %v", err)
			}
			if lifecycle.ProcessMode != tt.processMode || lifecycle.RestartMode != tt.restartMode ||
				lifecycle.AllowDependencies != tt.allowDependencies || lifecycle.SocketActivated != tt.socketActivated ||
				!slices.Equal(lifecycle.AuxiliaryUnits, tt.auxiliaryUnits) || !slices.Equal(lifecycle.DelegatedRoles, tt.delegatedRoles) {
				t.Fatalf("lifecycle = %+v, want process=%q restart=%q auxiliary=%v dependencies=%t socket=%t delegated=%v", lifecycle, tt.processMode, tt.restartMode, tt.auxiliaryUnits, tt.allowDependencies, tt.socketActivated, tt.delegatedRoles)
			}
		})
	}
}
