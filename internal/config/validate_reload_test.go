package config

import (
	"fmt"
	"slices"
	"strings"
	"syscall"
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

func TestParseReload(t *testing.T) {
	tests := []struct {
		name       string
		tree       map[string]any
		configured bool
		hasSignal  bool
		signal     syscall.Signal
		command    []string
		always     bool
	}{
		{name: "absent", tree: map[string]any{}},
		{name: "signal", tree: map[string]any{"reload": map[string]any{"signal": "HUP"}}, configured: true, hasSignal: true, signal: syscall.SIGHUP},
		{name: "command always", tree: map[string]any{"reload": map[string]any{"command": []any{"nginx", "-s", "reload"}, "when": "always"}}, configured: true, command: []string{"nginx", "-s", "reload"}, always: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := ParseReload(tc.tree)
			if err != nil {
				t.Fatalf("ParseReload: %v", err)
			}
			if spec.Configured != tc.configured || spec.HasSignal != tc.hasSignal || spec.Signal != tc.signal || spec.Always != tc.always || !slices.Equal(spec.Command, tc.command) {
				t.Fatalf("ParseReload = %+v, want configured=%t signal=%d/%t command=%v always=%t", spec, tc.configured, tc.signal, tc.hasSignal, tc.command, tc.always)
			}
		})
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
		{"shell string command", map[string]any{"command": "nginx -s reload"}, "non-empty argv array"},
		{"empty command", map[string]any{"command": []any{}}, "non-empty argv array"},
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
		"reload.signal requires top-level pidfile:",
		"reload.signal requires a processes selector with both exe and user",
	} {
		if !strings.Contains(strings.Join(bad, "\n"), want) {
			t.Fatalf("missing issue %q in:\n%s", want, strings.Join(bad, "\n"))
		}
	}

	ok := reloadTreeIssues(t, "openrc", map[string]any{
		"pidfile": "/run/svc.pid",
		"reload":  map[string]any{"signal": "HUP"},
		"processes": map[string]any{
			"identity": map[string]any{"exe": "/usr/sbin/svc", "user": "svc"},
		},
	})
	if len(ok) != 0 {
		t.Fatalf("valid OpenRC signal reload flagged: %v", ok)
	}

	systemdOnly := reloadTreeIssues(t, "openrc", map[string]any{
		"service": map[string]any{"systemd": []any{"patroni"}},
		"reload":  map[string]any{"signal": "HUP"},
		"processes": map[string]any{
			"main": map[string]any{"exe": "/bin/patroni", "user": "postgres"},
		},
	})
	if len(systemdOnly) != 0 {
		t.Fatalf("systemd-only signal reload should not require OpenRC pidfile, got %v", systemdOnly)
	}

	systemd := reloadIssues(t, "systemd", map[string]any{"signal": "HUP"})
	if len(systemd) != 0 {
		t.Fatalf("systemd signal reload without pidfile should be valid, got %v", systemd)
	}
}
