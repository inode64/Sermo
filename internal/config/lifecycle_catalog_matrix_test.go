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
		auxiliaryUnits []AuxiliaryUnit
	}{
		{name: "docker systemd", service: "docker", backend: backendSystemd, processMode: ServiceProcessResident, auxiliaryUnits: []AuxiliaryUnit{{Unit: "docker.socket"}}},
		{name: "docker openrc", service: "docker", backend: backendOpenRC, processMode: ServiceProcessResident},
		{name: "nfs systemd", service: "nfs", backend: backendSystemd, processMode: ServiceProcessNone},
		{name: "nfs openrc", service: "nfs", backend: backendOpenRC, processMode: ServiceProcessNone},
		{name: "lvm monitor systemd", service: "lvm2-monitor", backend: backendSystemd, processMode: ServiceProcessNone},
		{name: "cockpit socket systemd", service: "cockpit", backend: backendSystemd, processMode: ServiceProcessInit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolved := resolveCatalogService(t, tt.service, tt.backend)
			lifecycle := ResolveServiceLifecycle(resolved.Tree, tt.backend)
			if lifecycle.ProcessMode != tt.processMode ||
				!slices.Equal(lifecycle.AuxiliaryUnits, tt.auxiliaryUnits) {
				t.Fatalf("lifecycle = %+v, want process=%q auxiliary=%v", lifecycle, tt.processMode, tt.auxiliaryUnits)
			}
		})
	}
}

func TestNetworkdOptionalDependent(t *testing.T) {
	resolved := resolveCatalogService(t, "systemd-networkd", backendSystemd)
	got := ResolveServiceLifecycle(resolved.Tree, backendSystemd).AuxiliaryUnits
	want := []AuxiliaryUnit{{Unit: "systemd-networkd-persistent-storage.service", Optional: true, StartAfterPrimary: true}}
	if !slices.Equal(got, want) {
		t.Fatalf("auxiliary units = %+v, want %+v", got, want)
	}
	if got := AdditionalUnits(resolved.Tree, backendOpenRC); len(got) != 0 {
		t.Fatalf("OpenRC units = %+v", got)
	}
}
