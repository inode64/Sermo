package config

import (
	"fmt"
	"strings"
	"testing"
)

func reloadIssues(t *testing.T, backend string, reload any) []string {
	t.Helper()
	return reloadTreeIssues(t, backend, map[string]any{"reload": reload})
}

func reloadTreeIssues(t *testing.T, backend string, tree map[string]any) []string {
	t.Helper()
	var issues []string
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }
	validateReload(tree, backend, add)
	return issues
}

func TestValidateReloadValid(t *testing.T) {
	cases := []any{
		map[string]any{"signal": "HUP"},
		map[string]any{"signal": "sighup", "when": "auto"},
		map[string]any{"command": []any{"nginx", "-s", "reload"}, "when": "always"},
	}
	for i, c := range cases {
		if issues := reloadIssues(t, "systemd", c); len(issues) != 0 {
			t.Errorf("case %d: expected no issues, got %v", i, issues)
		}
	}
}

func TestValidateReloadRejectsBadShapes(t *testing.T) {
	cases := []struct {
		name   string
		reload any
		want   string
	}{
		{"not a map", "HUP", "reload must be a mapping"},
		{"both signal and command", map[string]any{"signal": "HUP", "command": []any{"x"}}, "use exactly one"},
		{"neither", map[string]any{"when": "auto"}, "must set either signal or command"},
		{"unknown signal", map[string]any{"signal": "BOGUS"}, "not a known signal name"},
		{"bad when", map[string]any{"signal": "HUP", "when": "sometimes"}, "must be \"auto\" or \"always\""},
		{"shell string command", map[string]any{"command": "nginx -s reload"}, "must be an array"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			joined := strings.Join(reloadIssues(t, "systemd", tc.reload), "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("missing issue %q in:\n%s", tc.want, joined)
			}
		})
	}
}

// TestValidateReloadAbsent: no reload block means no issues.
func TestValidateReloadAbsent(t *testing.T) {
	var issues []string
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }
	validateReload(map[string]any{}, "systemd", add)
	if len(issues) != 0 {
		t.Errorf("expected no issues for absent reload, got %v", issues)
	}
}

func TestValidateReloadSignalRequiresPidfileIdentityOnOpenRC(t *testing.T) {
	bad := reloadTreeIssues(t, "openrc", map[string]any{
		"reload": map[string]any{"signal": "HUP"},
	})
	for _, want := range []string{
		"reload.signal requires a processes pidfile selector",
		"reload.signal requires a processes command_match selector with both exe and user",
	} {
		if !strings.Contains(strings.Join(bad, "\n"), want) {
			t.Fatalf("missing issue %q in:\n%s", want, strings.Join(bad, "\n"))
		}
	}

	ok := reloadTreeIssues(t, "openrc", map[string]any{
		"reload": map[string]any{"signal": "HUP"},
		"processes": map[string]any{
			"main":     map[string]any{"type": "pidfile", "path": "/run/svc.pid"},
			"identity": map[string]any{"type": "command_match", "exe": "/usr/sbin/svc", "user": "svc"},
		},
	})
	if len(ok) != 0 {
		t.Fatalf("valid OpenRC signal reload flagged: %v", ok)
	}

	systemd := reloadIssues(t, "systemd", map[string]any{"signal": "HUP"})
	if len(systemd) != 0 {
		t.Fatalf("systemd signal reload without pidfile should be valid, got %v", systemd)
	}
}
