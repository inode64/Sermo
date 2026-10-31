package app

import (
	"testing"

	"sermo/internal/config"
	"sermo/internal/state"
)

// A daemon restart or reload must not undo a storage umount: the mount is
// released on purpose, so forcing its watch on reports it as down until the next
// mount. Any other pause stays subject to `monitor: enabled`.
func TestMonitorEnabledKeepsOnlyTheStorageUmountPause(t *testing.T) {
	const key = "watch:storage"
	cases := []struct {
		name       string
		source     string
		wantActive bool
		wantSource string
	}{
		{name: "cli umount", source: state.SourceCLIMountUmount, wantSource: state.SourceCLIMountUmount},
		{name: "web umount", source: state.SourceWebMountUmount, wantSource: state.SourceWebMountUmount},
		{name: "operator pause", source: state.SourceCLI, wantActive: true, wantSource: state.SourceConfig},
		{name: "manual stop", source: state.SourceCLIManualStop, wantActive: true, wantSource: state.SourceConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			if err := store.SetActive(key, false, tc.source); err != nil {
				t.Fatal(err)
			}
			if warning := applyMonitorModeFor(store, "watch storage", key, config.MonitorEnabled); warning != "" {
				t.Fatal(warning)
			}
			if store.active[key] != tc.wantActive || store.source[key] != tc.wantSource {
				t.Fatalf("active = %v from %q, want %v from %q", store.active[key], store.source[key], tc.wantActive, tc.wantSource)
			}
		})
	}
}

// A row that is already active, or none at all, is still written, so the source
// keeps naming the configuration that decided it.
func TestMonitorEnabledRecordsTheConfiguredState(t *testing.T) {
	const key = "watch:storage"
	store := newFakeStore()
	if warning := applyMonitorModeFor(store, "watch storage", key, config.MonitorEnabled); warning != "" {
		t.Fatal(warning)
	}
	if !store.active[key] || store.source[key] != state.SourceConfig {
		t.Fatalf("active = %v from %q, want true from config", store.active[key], store.source[key])
	}
}
