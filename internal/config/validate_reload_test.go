package config

import (
	"fmt"
	"strings"
	"testing"
)

func reloadIssues(t *testing.T, reload any) []string {
	t.Helper()
	var issues []string
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }
	validateReload(map[string]any{"reload": reload}, add)
	return issues
}

func TestValidateReloadValid(t *testing.T) {
	cases := []any{
		map[string]any{"signal": "HUP"},
		map[string]any{"signal": "sighup", "when": "auto"},
		map[string]any{"command": []any{"nginx", "-s", "reload"}, "when": "always"},
	}
	for i, c := range cases {
		if issues := reloadIssues(t, c); len(issues) != 0 {
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
			joined := strings.Join(reloadIssues(t, tc.reload), "\n")
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
	validateReload(map[string]any{}, add)
	if len(issues) != 0 {
		t.Errorf("expected no issues for absent reload, got %v", issues)
	}
}
