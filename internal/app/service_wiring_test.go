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
