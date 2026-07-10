package config

import (
	"testing"

	"sermo/internal/emission"
)

func TestValidateEmissionScopes(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		emission.Section: map[string]any{
			emission.KeyEvents: emission.ModeOnChange,
			emission.KeyNotify: emission.ModeEveryCycle,
		},
		"watches": map[string]any{
			"storage-root": map[string]any{
				"check": map[string]any{
					"type":     "storage",
					"path":     "/",
					"used_pct": map[string]any{"op": ">", "value": 90},
				},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/storage-alert"}}},
				emission.Section: map[string]any{
					emission.KeyEvents: emission.ModeEveryCycle,
				},
			},
		},
		"rules": map[string]any{
			"warn-down": map[string]any{
				"type": "alert",
				"if":   map[string]any{"failed": map[string]any{"check": "http"}},
				"then": map[string]any{"action": "alert", "message": "down"},
				emission.Section: map[string]any{
					emission.KeyNotify: emission.ModeEveryCycle,
				},
			},
		},
	})
	for _, issue := range issues {
		if issue.Msg != "" && hasIssue([]Issue{issue}, emission.Section) {
			t.Fatalf("valid emission scopes produced issue: %v", issues)
		}
	}
}

func TestValidateEmissionRejectsInvalidMode(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		emission.Section: map[string]any{emission.KeyEvents: "always"},
	})
	if !hasIssue(issues, `emission.events "always" is not one of on_change, every_cycle`) {
		t.Fatalf("Validate issues = %v, want invalid global emission mode", issues)
	}

	issues = validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"storage-root": map[string]any{
				"check": map[string]any{
					"type":     "storage",
					"path":     "/",
					"used_pct": map[string]any{"op": ">", "value": 90},
				},
				emission.Section: map[string]any{emission.KeyNotify: "always"},
			},
		},
	})
	if !hasIssue(issues, `watches.storage-root.emission.notify "always" is not one of on_change, every_cycle`) {
		t.Fatalf("Validate issues = %v, want invalid watch emission mode", issues)
	}

	issues = validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"uplink": map[string]any{
				"check": map[string]any{"type": "net", "interface": "eth0"},
				"metrics": map[string]any{
					"state": map[string]any{
						"expect":         "up",
						emission.Section: map[string]any{emission.KeyEvents: "always"},
					},
				},
			},
		},
	})
	if !hasIssue(issues, `watches.uplink.metrics.state.emission.events "always" is not one of on_change, every_cycle`) {
		t.Fatalf("Validate issues = %v, want invalid metric-watch emission mode", issues)
	}
}

func TestValidateResolvedRejectsServiceEmission(t *testing.T) {
	issues := validateResolved("web", map[string]any{
		"name":    "web",
		"service": "web",
		"policy":  map[string]any{"cooldown": "5m"},
		emission.Section: map[string]any{
			emission.KeyEvents: emission.ModeEveryCycle,
		},
	}, "/run/sermo", nil, map[string]struct{}{"web": {}}, backendSystemd)
	if !hasIssue(issues, "emission is not supported on services") {
		t.Fatalf("validateResolved issues = %v, want service emission rejection", issues)
	}
}
