package app

import (
	"testing"

	"sermo/internal/execx"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// A filterless process_count inside a service must count what discovery
// attributes to that service, never the whole host: with selectors that match
// nothing (a crashed service whose binary is gone), the count is zero even
// though the host is full of processes. Host scope here once turned "fcron has
// active jobs" into 362 and latched its own block action.
func TestServiceScopedProcessCountNeverCountsTheHost(t *testing.T) {
	tree := map[string]any{
		"processes": map[string]any{
			"main": map[string]any{"exe": "/nonexistent/uninstalled-daemon", "user": "root"},
		},
	}
	scoped := ServiceScopedProcessCount(t.Context(), tree, execx.CommandRunner{}, servicemgr.BackendOpenRC, "gone", process.NewDiscovererWithUserLookup(nil))
	if got := scoped("", "", ""); got != 0 {
		t.Fatalf("scoped filterless count = %d, want 0 (selectors match nothing)", got)
	}
	// The host-wide count the old wiring used is emphatically not zero.
	if host := process.NewDiscovererWithUserLookup(nil).CountMatching("", "", ""); host == 0 {
		t.Fatal("sanity: host has processes")
	}
}

func TestServiceBackendPIDsUsesOnlyResolvedProviders(t *testing.T) {
	explicit := func() []int { return []int{42} }
	tests := []struct {
		name       string
		backend    servicemgr.Backend
		configured func() []int
		wantNil    bool
		wantPID    int
	}{
		{name: "systemd derives", backend: servicemgr.BackendSystemd},
		{name: "openrc derives", backend: servicemgr.BackendOpenRC},
		{name: "docker without provider", backend: servicemgr.BackendDocker, wantNil: true},
		{name: "libvirt without provider", backend: servicemgr.BackendLibvirt, wantNil: true},
		{name: "explicit controlled provider wins", backend: servicemgr.BackendDocker, configured: explicit, wantPID: 42},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ServiceBackendPIDs(t.Context(), tc.backend, "svc", tc.configured, execx.CommandRunner{})
			if tc.wantNil {
				if got != nil {
					t.Fatal("ServiceBackendPIDs() is non-nil")
				}
				return
			}
			if got == nil {
				t.Fatal("ServiceBackendPIDs() is nil")
			}
			if tc.wantPID > 0 {
				pids := got()
				if len(pids) != 1 || pids[0] != tc.wantPID {
					t.Fatalf("ServiceBackendPIDs() = %v, want [%d]", pids, tc.wantPID)
				}
			}
		})
	}
}
