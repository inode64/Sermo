package config

import (
	"strings"
	"testing"
)

func TestValidateHdparmFields(t *testing.T) {
	// Missing device and no predicate -> two issues.
	issues := collect(func(add func(string, ...any)) {
		validateHdparmFields("checks.disk-speed", map[string]any{}, add)
	})
	joined := strings.Join(issues, "\n")
	for _, want := range []string{"device is required", "requires at least one of read/cached"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, issues)
		}
	}

	// Invalid op on a present predicate.
	bad := collect(func(add func(string, ...any)) {
		validateHdparmFields("checks.disk-speed", map[string]any{
			"device": "/dev/sda",
			"read":   map[string]any{"op": "=>", "value": 100},
		}, add)
	})
	if len(bad) == 0 {
		t.Error("an invalid predicate op should be reported")
	}

	// Valid: device + one predicate.
	ok := collect(func(add func(string, ...any)) {
		validateHdparmFields("checks.disk-speed", map[string]any{
			"device": "/dev/sda",
			"read":   map[string]any{"op": "<", "value": 100},
			"cached": map[string]any{"op": "<", "value": 3000},
		}, add)
	})
	if len(ok) != 0 {
		t.Errorf("valid hdparm check should have no issues, got: %v", ok)
	}
}
