package config

import (
	"slices"
	"testing"
)

func TestRealCatalogLifecycleBackendMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		service        string
		backend        string
		processMode    ServiceProcessMode
		restartMode    RestartMode
		auxiliaryUnits []string
	}{
		{name: "docker systemd", service: "docker", backend: backendSystemd, processMode: ServiceProcessResident, restartMode: RestartModeNative, auxiliaryUnits: []string{"docker.socket"}},
		{name: "docker openrc", service: "docker", backend: backendOpenRC, processMode: ServiceProcessResident, restartMode: RestartModeNative},
		{name: "nfs systemd", service: "nfs", backend: backendSystemd, processMode: ServiceProcessNone, restartMode: RestartModeStaged},
		{name: "nfs openrc", service: "nfs", backend: backendOpenRC, processMode: ServiceProcessNone, restartMode: RestartModeStaged},
		{name: "lvm monitor systemd", service: "lvm2-monitor", backend: backendSystemd, processMode: ServiceProcessNone, restartMode: RestartModeStaged},
		{name: "cockpit socket systemd", service: "cockpit", backend: backendSystemd, processMode: ServiceProcessInit, restartMode: RestartModeStaged},
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
				!slices.Equal(lifecycle.AuxiliaryUnits, tt.auxiliaryUnits) {
				t.Fatalf("lifecycle = %+v, want process=%q restart=%q auxiliary=%v", lifecycle, tt.processMode, tt.restartMode, tt.auxiliaryUnits)
			}
		})
	}
}
