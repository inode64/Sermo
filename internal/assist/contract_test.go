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
		Monitoring: Monitoring{Monitor: "previous", Interval: "1m"},
		metric:     "free_pct", op: "<", value: "10%",
		forCycles: 3, notifiers: []string{"ops"},
		dryRun: true, expand: true, expandBy: "5G", cooldown: "30m",
	})
	volBytes := buildVolWatch(Volume{Mountpoint: "/srv"}, volSettings{
		Monitoring: Monitoring{Monitor: "disabled"},
		metric:     "used_bytes", op: ">=", value: "100G",
		notifiers: []string{"none"}, // monitor-only watch must also validate
	})
	netAll := buildNetWatch(Iface{Name: "eth0"}, netSettings{
		Monitoring: Monitoring{Monitor: "enabled", Interval: "15s"},
		metrics:    []string{"state", "errors", "speed", "address"},
		errorsAt:   100, stateDown: true,
		notifiers: []string{"ops"},
	})
	uplink := buildUplinkWatches("ppp0", uplinkSettings{
		probeHost: "1.1.1.1", probeName: "example.com", forCycles: 3,
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
	for name, entry := range uplink {
		generated[name] = entry
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

func TestGeneratedGenericServicePassesConfigValidation(t *testing.T) {
	cfg := &config.Config{
		Global: config.Global{
			Raw:      map[string]any{},
			Defaults: map[string]any{"policy": map[string]any{"cooldown": "5m"}},
		},
		Services: map[string]*config.Document{
			"customd": {
				Kind: "service",
				Name: "customd",
				Body: map[string]any{
					"enabled": true,
					"service": map[string]any{"name": "customd"},
					"checks": map[string]any{
						"service": map[string]any{"type": "service", "expect": "active"},
						"config": map[string]any{
							"type":      "config",
							"path":      []any{"/etc/customd.conf"},
							"on_change": true,
							"interval":  serviceConfigCheckInterval,
						},
					},
					"pidfile": "/run/customd.pid",
					"remediation": map[string]any{
						"shadow": true,
					},
				},
			},
		},
		ServiceNames: []string{"customd"},
	}
	for _, issue := range config.Validate(cfg) {
		if issue.Scope == "customd" {
			t.Errorf("wizard-generated generic service failed validation: %s", issue)
		}
	}
}

func TestGeneratedControlledServicesPassConfigValidation(t *testing.T) {
	cfg := &config.Config{
		Global: config.Global{
			Raw:      map[string]any{},
			Defaults: map[string]any{"policy": map[string]any{"cooldown": "5m"}},
		},
		Services: map[string]*config.Document{
			"docker-web": {
				Kind: "service",
				Name: "docker-web",
				Body: buildDockerService(DockerCandidate{
					Name:      "docker-web",
					Container: "web",
					Socket:    "/run/docker.sock",
				}),
			},
			"vm-web01": {
				Kind: "service",
				Name: "vm-web01",
				Body: buildVMService(VMCandidate{
					Name:   "vm-web01",
					Domain: "web01",
					URI:    "qemu:///system",
					Socket: "/run/libvirt/libvirt-sock",
				}),
			},
		},
		ServiceNames: []string{"docker-web", "vm-web01"},
	}
	for _, issue := range config.Validate(cfg) {
		if issue.Scope == "docker-web" || issue.Scope == "vm-web01" {
			t.Errorf("wizard-generated controlled service failed validation: %s", issue)
		}
	}
}
