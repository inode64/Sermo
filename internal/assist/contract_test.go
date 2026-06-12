package assist

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"sermo/internal/config"
)

// The wizard's generated watches must always pass config validation — this is
// the contract between the two surfaces (CLAUDE.md: no divergent grammars).
// Every builder/threshold form the wizard can emit is exercised here.
func TestGeneratedWatchesPassConfigValidation(t *testing.T) {
	volPct := buildVolWatch(Volume{Mountpoint: "/data"}, volSettings{
		metric: "free_pct", op: "<", value: "10%",
		forCycles: 3, notifiers: []string{"ops"},
		expand: true, expandBy: "5G", cooldown: "30m",
	})
	volBytes := buildVolWatch(Volume{Mountpoint: "/srv"}, volSettings{
		metric: "used_bytes", op: ">=", value: "100G",
		notifiers: []string{"ops"},
	})
	netAll := buildNetWatch(Iface{Name: "eth0"}, netSettings{
		metrics:  []string{"state", "errors", "speed"},
		errorsAt: 100, stateDown: true,
		notifiers: []string{"ops"},
	})

	// Round-trip through YAML, exactly like the wizard's written fragments are
	// read back by the loader — the validator sees parsed-YAML shapes, not the
	// builder's Go values.
	generated := map[string]any{
		"data":     volPct,
		"srv":      volBytes,
		"net-eth0": netAll,
	}
	text, err := yaml.Marshal(generated)
	if err != nil {
		t.Fatalf("marshal generated watches: %v", err)
	}
	var watches map[string]any
	if err := yaml.Unmarshal(text, &watches); err != nil {
		t.Fatalf("unmarshal generated watches: %v", err)
	}

	cfg := &config.Config{Global: config.Global{
		Raw: map[string]any{
			"notifiers": map[string]any{
				"ops": map[string]any{"type": "slack", "webhook": "https://hooks.slack.com/services/T0/B0/X"},
			},
			"watches": watches,
		},
		Defaults: map[string]any{"policy": map[string]any{"cooldown": "5m"}},
	}}
	for _, issue := range config.Validate(cfg) {
		if strings.Contains(issue.Msg, "watches.") {
			t.Errorf("wizard-generated watch failed validation: %s", issue)
		}
	}
}
