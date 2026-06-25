package config

import (
	"strings"
	"testing"
)

func TestValidateServiceMonitors(t *testing.T) {
	defined := map[string]struct{}{"ops": {}}

	// Unknown notifier in version.on_change.notify -> issue.
	bad := collect(func(add func(string, ...any)) {
		validateServiceMonitors(map[string]any{
			"version": map[string]any{"on_change": map[string]any{"notify": []any{"ghost"}}},
		}, defined, add)
	})
	if !strings.Contains(strings.Join(bad, "\n"), "version.on_change.notify references unknown notifier") {
		t.Errorf("expected unknown-notifier issue, got: %v", bad)
	}

	// Valid version + config blocks -> no issues.
	ok := collect(func(add func(string, ...any)) {
		validateServiceMonitors(map[string]any{
			"version": map[string]any{"on_change": map[string]any{"notify": []any{"ops"}}},
			"config":  map[string]any{"on_change": map[string]any{"notify": []any{"none"}}},
		}, defined, add)
	})
	if len(ok) != 0 {
		t.Errorf("valid monitors should have no issues, got: %v", ok)
	}

	// on_change must be a mapping.
	shape := collect(func(add func(string, ...any)) {
		validateServiceMonitors(map[string]any{"config": map[string]any{"on_change": "yes"}}, defined, add)
	})
	if len(shape) == 0 {
		t.Error("a non-mapping on_change should be reported")
	}

	// A valid version.on_change.level passes.
	if got := collect(func(add func(string, ...any)) {
		validateServiceMonitors(map[string]any{
			"version": map[string]any{"on_change": map[string]any{"notify": []any{"ops"}, "level": "minor"}},
		}, defined, add)
	}); len(got) != 0 {
		t.Errorf("level: minor should be valid, got: %v", got)
	}

	// An invalid level value -> issue.
	if got := collect(func(add func(string, ...any)) {
		validateServiceMonitors(map[string]any{
			"version": map[string]any{"on_change": map[string]any{"level": "epoch"}},
		}, defined, add)
	}); !strings.Contains(strings.Join(got, "\n"), "level \"epoch\" is not one of major, minor, patch") {
		t.Errorf("expected invalid-level issue, got: %v", got)
	}

	// level on the config monitor is rejected (version-only).
	if got := collect(func(add func(string, ...any)) {
		validateServiceMonitors(map[string]any{
			"config": map[string]any{"on_change": map[string]any{"level": "minor"}},
		}, defined, add)
	}); !strings.Contains(strings.Join(got, "\n"), "level is only supported for the version monitor") {
		t.Errorf("expected version-only-level issue, got: %v", got)
	}
}
