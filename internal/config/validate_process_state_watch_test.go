package config

import (
	"maps"
	"testing"
)

func TestValidateProcessStateWatch(t *testing.T) {
	base := map[string]any{"type": "process", "exe": "/usr/bin/worker", "user": "0", "state": "running"}
	for _, state := range []string{"running", "absent", "zombie"} {
		t.Run(state, func(t *testing.T) {
			check := maps.Clone(base)
			check["state"] = state
			entry := map[string]any{"check": check, "for": map[string]any{"cycles": 2}, "clear": map[string]any{"cycles": 2}}
			assertNoWatchIssues(t, map[string]any{"watches": map[string]any{"worker": entry}})
		})
	}
	for _, tc := range []struct {
		name  string
		key   string
		value any
	}{
		{"empty state", "state", ""}, {"non-string state", "state", true}, {"invalid state", "state", "stopped"},
		{"relative executable", "exe", "worker"}, {"mixed name", "name", "worker"},
		{"mixed gone", "gone", true}, {"mixed duration", "for", "1m"},
		{"mixed threshold", "cpu", map[string]any{"op": ">", "value": 90}},
		{"invalid cmd", "cmd", "["},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := maps.Clone(base)
			check[tc.key] = tc.value
			assertEachWatchInvalid(t, "worker", map[string]map[string]any{tc.name: {"check": check}})
		})
	}
	assertEachWatchInvalid(t, "worker", map[string]map[string]any{
		"missing selector": {"check": map[string]any{"type": "process", "state": "running"}},
		"kill":             {"check": base, "then": map[string]any{"kill": map[string]any{"signal": "TERM"}}},
	})
	assertNoWatchIssues(t, watchConfig("worker", map[string]any{
		"type": "process", "cmd": "(^| )worker( |$)", "state": "running",
	}))
	assertEachWatchInvalid(t, "worker", map[string]map[string]any{
		"relative alternative": {"check": map[string]any{"type": "process", "exe_any": []any{"worker"}, "state": "running"}},
	})
}
