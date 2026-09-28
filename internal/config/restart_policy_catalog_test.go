package config

import (
	"testing"

	"sermo/internal/process"
)

// Delegated workloads survive stop/start. The daemon itself remains managed.
func TestDelegatedWorkloadCatalogPreservesWorkloads(t *testing.T) {
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

		})
	}
}

// libvirt's dnsmasq processes serve guests across a daemon restart.
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
