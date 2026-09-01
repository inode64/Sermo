package config

import (
	"fmt"
	"testing"
)

func TestValidateGlobalEngineUsesCanonicalBackendParser(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		backend string
		valid   bool
	}{
		{"", true},
		{"auto", true},
		{"systemd", true},
		{"openrc", true},
		{" SYSTEMD ", true},
		{"bogus", false},
	} {
		var issues []string
		validateGlobalEngine(&Config{}, map[string]any{
			SectionEngine: map[string]any{EngineKeyBackend: test.backend},
		}, func(format string, args ...any) {
			issues = append(issues, fmt.Sprintf(format, args...))
		})
		if test.valid && len(issues) != 0 {
			t.Errorf("backend %q issues = %v", test.backend, issues)
		}
		if !test.valid && len(issues) != 1 {
			t.Errorf("backend %q issues = %v, want one", test.backend, issues)
		}
	}
}
