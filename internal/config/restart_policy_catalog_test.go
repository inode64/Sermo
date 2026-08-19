package config

import (
	"testing"

	"sermo/internal/process"
)

// Container runtimes and glusterd own delegated process trees whose continued
// presence is not a failed daemon stop. Their catalog profiles therefore
// delegate restart atomically to the init backend instead of treating those
// workload processes as residuals between Stop and Start. Ordinary
// multi-process daemons keep the staged default.
func TestDelegatedWorkloadCatalogUsesNativeRestart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		service        string
		delegatedRoles []string
	}{
		{service: "containerd", delegatedRoles: []string{"shim"}},
		{service: "docker", delegatedRoles: []string{"proxy"}},
		{service: "glusterd", delegatedRoles: []string{"brick", "selfheal", "daemon"}},
	}
	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			t.Parallel()

			byRole := catalogSelectorsByRole(t, tt.service)
			for _, role := range tt.delegatedRoles {
				selector, ok := byRole[role]
				if !ok {
					t.Fatalf("%s declares no %q process role", tt.service, role)
				}
				if !selector.Delegated {
					t.Fatalf("%s role %q must be delegated: a reconciling restart would otherwise signal the workload", tt.service, role)
				}
			}
			if byRole[process.RoleMain].Delegated {
				t.Fatalf("%s must keep its own daemon signallable", tt.service)
			}

			for _, backend := range []string{backendSystemd, backendOpenRC} {
				resolved := resolveCatalogService(t, tt.service, backend)
				got, err := ParseRestartMode(resolved.Tree)
				if err != nil {
					t.Fatalf("ParseRestartMode(%s, %s): %v", tt.service, backend, err)
				}
				if got != RestartModeNative {
					t.Fatalf("ParseRestartMode(%s, %s) = %q, want %q", tt.service, backend, got, RestartModeNative)
				}
			}
		})
	}
}

func TestPolkitCatalogUsesNativeRestartForDBusActivation(t *testing.T) {
	t.Parallel()

	resolved := resolveCatalogService(t, "polkit", backendSystemd)
	got, err := ParseRestartMode(resolved.Tree)
	if err != nil {
		t.Fatalf("ParseRestartMode(polkit, systemd): %v", err)
	}
	if got != RestartModeNative {
		t.Fatalf("ParseRestartMode(polkit, systemd) = %q, want %q", got, RestartModeNative)
	}
}

// virtnetworkd keeps the staged default, so it stays out of the native-restart
// table above — but the same delegation rule applies. libvirt spawns one dnsmasq
// pair per virtual network; they serve the guests' DHCP and DNS across a daemon
// restart, and a package update routinely replaces their binary without one. An
// undelegated pair is discovered as a residual, becomes unsignallable the moment
// its exe stops resolving, and parks every restart in orphan_processes.
func TestVirtnetworkdCatalogDelegatesItsDnsmasqPair(t *testing.T) {
	t.Parallel()

	byRole := catalogSelectorsByRole(t, "virtnetworkd")
	for _, role := range []string{"dnsmasq-root", "dnsmasq-nobody"} {
		selector, ok := byRole[role]
		if !ok {
			t.Fatalf("virtnetworkd declares no %q process role", role)
		}
		if !selector.Delegated {
			t.Fatalf("virtnetworkd role %q must be delegated so a stop never signals the guests' addressing", role)
		}
	}
	if byRole[process.RoleMain].Delegated {
		t.Fatalf("virtnetworkd must keep its own daemon signallable")
	}
}

func TestDockerCatalogKeepsSystemdSocketForExplicitStartStop(t *testing.T) {
	t.Parallel()

	resolved := resolveCatalogService(t, "docker", backendSystemd)
	units := AdditionalUnits(resolved.Tree, backendSystemd)
	if len(units) != 1 || units[0] != "docker.socket" {
		t.Fatalf("AdditionalUnits(docker) = %v, want [docker.socket]", units)
	}
}
