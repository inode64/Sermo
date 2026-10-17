package config

import (
	"fmt"
	"testing"

	"sermo/internal/checks"
)

func TestValidateStaleLibraries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry map[string]any
		fail  bool
	}{
		{"defaults", map[string]any{}, false},
		{"window", map[string]any{checks.CheckKeyStableFor: "5m"}, false},
		{"zero", map[string]any{checks.CheckKeyStableFor: "0s"}, true},
		{"negative", map[string]any{checks.CheckKeyStableFor: "-5m"}, true},
		{"invalid", map[string]any{checks.CheckKeyStableFor: "eventually"}, true},
		{"selector", map[string]any{checks.CheckKeyExe: "/bin/sshd"}, true},
		{"scope", map[string]any{checks.CheckKeyScope: "system"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var issues []string
			validateStaleLibrariesCheck("checks.libraries", tc.entry, func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) })
			if (len(issues) > 0) != tc.fail {
				t.Fatalf("issues: %v", issues)
			}
		})
	}
}
