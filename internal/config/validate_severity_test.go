package config

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"sermo/internal/checks"
)

// A watch file has no key allow-list, so an unwired `severity:` would be
// accepted in silence rather than rejected. Every surface that accepts it must
// therefore validate it explicitly.
func TestValidateWatchSeveritySurfaces(t *testing.T) {
	valid := map[string]any{
		"check": map[string]any{
			"type": checks.CheckTypeHdparm, "device": "/dev/sdd",
			"read": map[string]any{"op": "<", "value": 20},
		},
		"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
	}
	for _, sev := range []string{checks.SeverityError, checks.SeverityWarning} {
		w := map[string]any{"severity": sev, "check": valid["check"], "then": valid["then"]}
		assertNoWatchIssues(t, map[string]any{"watches": map[string]any{"hdparm-sdd": w}})
	}

	t.Run("watch entry", func(t *testing.T) {
		w := map[string]any{"severity": "urgent", "check": valid["check"], "then": valid["then"]}
		assertWatchIssues(t, map[string]any{"watches": map[string]any{"hdparm-sdd": w}}, "severity")
	})

	t.Run("check block", func(t *testing.T) {
		check := map[string]any{
			"type": checks.CheckTypeHdparm, "device": "/dev/sdd",
			"read": map[string]any{"op": "<", "value": 20}, "severity": "urgent",
		}
		assertWatchIssues(t, watchConfig("hdparm-sdd", check), "severity")
	})

	// `ok` grades an analyze match, never a check: a check that has nothing to
	// say does not fail in the first place.
	t.Run("ok is not a check severity", func(t *testing.T) {
		w := map[string]any{"severity": checks.SeverityOK, "check": valid["check"], "then": valid["then"]}
		assertWatchIssues(t, map[string]any{"watches": map[string]any{"hdparm-sdd": w}}, "severity")
	})

	t.Run("metric block", func(t *testing.T) {
		w := map[string]any{
			"check": map[string]any{"type": checks.CheckTypeNet, "interface": "enp1s0"},
			"metrics": map[string]any{
				"errors": map[string]any{
					"severity": "urgent",
					"delta":    map[string]any{"op": ">", "value": 100},
					"then":     map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
				},
			},
		}
		assertWatchIssues(t, map[string]any{"watches": map[string]any{"net-enp1s0": w}}, "severity")
	})

	t.Run("valid metric block", func(t *testing.T) {
		w := map[string]any{
			"check": map[string]any{"type": checks.CheckTypeNet, "interface": "enp1s0"},
			"metrics": map[string]any{
				"errors": map[string]any{
					"severity": checks.SeverityWarning,
					"delta":    map[string]any{"op": ">", "value": 100},
					"then":     map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
				},
			},
		}
		assertNoWatchIssues(t, map[string]any{"watches": map[string]any{"net-enp1s0": w}})
	})
}

// The same declaration must behave the same way in a service's checks: section
// as it does in a host watch.
func TestValidateServiceCheckSeverity(t *testing.T) {
	tree := map[string]any{
		"checks": map[string]any{
			"disk-speed": map[string]any{
				"type": checks.CheckTypeHdparm, "device": "/dev/sda",
				"read": map[string]any{"op": "<", "value": 100}, "severity": "urgent",
			},
		},
	}
	var issues []string
	validateCheckSection(tree, "checks", "", func(format string, args ...any) {
		issues = append(issues, fmt.Sprintf(format, args...))
	})
	if !slices.ContainsFunc(issues, func(msg string) bool { return strings.Contains(msg, "severity") }) {
		t.Fatalf("expected a severity issue, got %v", issues)
	}
}
