package app

import (
	"testing"

	"sermo/internal/config"
	"sermo/internal/state"
)

// A daemon restart or reload must not undo an intentional stop or umount.
// Explicit unmonitor requests remain subject to `monitor: enabled`.
func TestMonitorEnabledKeepsOperationPauses(t *testing.T) {
	cases := []struct {
		name       string
		key        string
		source     string
		wantActive bool
		wantSource string
	}{
		{name: "cli umount", key: "watch:storage", source: state.SourceCLIMountUmount, wantSource: state.SourceCLIMountUmount},
		{name: "web umount", key: "watch:storage", source: state.SourceWebMountUmount, wantSource: state.SourceWebMountUmount},
		{name: "watch unmonitor", key: "watch:storage", source: state.SourceCLI, wantActive: true, wantSource: state.SourceConfig},
		{name: "cli stop", key: "web", source: state.SourceCLIManualStop, wantSource: state.SourceCLIManualStop},
		{name: "web stop", key: "web", source: state.SourceWebManualStop, wantSource: state.SourceWebManualStop},
		{name: "service unmonitor", key: "web", source: state.SourceWeb, wantActive: true, wantSource: state.SourceConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			if err := store.SetActive(tc.key, false, tc.source); err != nil {
				t.Fatal(err)
			}
			if warning := applyMonitorModeFor(store, tc.key, tc.key, config.MonitorEnabled); warning != "" {
				t.Fatal(warning)
			}
			if store.active[tc.key] != tc.wantActive || store.source[tc.key] != tc.wantSource {
				t.Fatalf("active = %v from %q, want %v from %q", store.active[tc.key], store.source[tc.key], tc.wantActive, tc.wantSource)
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
