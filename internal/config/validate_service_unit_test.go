package config

import "testing"

func TestExplicitServiceWatch(t *testing.T) {
	assertNoWatchIssues(t, watchConfig("unit", map[string]any{"type": "service", "service": "app.service", "backend": "systemd", "expect": "active"}))
	assertNoWatchIssues(t, watchConfig("unit", map[string]any{"type": "service", "service": "app", "backend": "systemd", "expect_result": "success"}))
	for _, check := range []map[string]any{
		{"type": "service", "service": "../app", "backend": "openrc", "expect": "active"},
		{"type": "service", "service": "app", "backend": "other", "expect": "active"},
		{"type": "service", "service": "app", "backend": "openrc", "expect_result": "success"},
		{"type": "service", "service": "app", "backend": "systemd", "expect_result": "success", "expect": "active"},
	} {
		assertWatchIssues(t, watchConfig("unit", check), "watches.unit.check")
	}
}
