package assist

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/notify"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
)

// The wizard's generated watches must always pass config validation — this is
// the contract between the two surfaces (CLAUDE.md: no divergent grammars).
// Every builder/threshold form the wizard can emit is exercised here.
func TestGeneratedWatchesPassConfigValidation(t *testing.T) {
	volPct := buildVolWatch(Volume{Mountpoint: "/data"}, volSettings{
		Monitoring: Monitoring{Monitor: config.MonitorPrevious, Interval: "1m"},
		metric:     checks.LevelFieldFreePct, op: cfgval.CompareOpLess, value: "10%",
		forCycles: volumeDefaultForCycles, notifiers: []string{"ops"},
		dryRun: true, expand: true, expandBy: volumeDefaultExpandBy, cooldown: volumeDefaultExpandCooldown,
	})
	volBytes := buildVolWatch(Volume{Mountpoint: "/srv"}, volSettings{
		Monitoring: Monitoring{Monitor: config.MonitorDisabled},
		metric:     checks.LevelFieldUsedBytes, op: cfgval.CompareOpGreaterEqual, value: volumeDefaultUsedSize,
		notifiers: []string{config.NotifyNone}, // monitor-only watch must also validate
	})
	netAll := buildNetWatch(Iface{Name: "eth0"}, netSettings{
		Monitoring: Monitoring{Monitor: config.MonitorEnabled, Interval: "15s"},
		metrics:    []string{checks.NetMetricState, checks.NetMetricErrors, checks.NetMetricSpeed, checks.NetMetricAddress},
		errorsAt:   netDefaultErrorDelta, stateDown: true,
		notifiers: []string{"ops"},
	})
	uplink := buildUplinkWatches("ppp0", uplinkSettings{
		probeHost: uplinkDefaultProbeHost, probeName: uplinkDefaultProbeName, forCycles: uplinkDefaultForCycles,
		notifiers: []string{"ops"},
	})

	// Round-trip through YAML, exactly like the wizard's written documents are
	// read back by the loader — the validator sees parsed-YAML shapes, not the
	// builder's Go values.
	generated := map[string]any{
		"data":                  volPct,
		"srv":                   volBytes,
		netWatchPrefix + "eth0": netAll,
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
				"ops": map[string]any{notify.KeyType: notify.TypeSlack, notify.KeyWebhook: "https://hooks.slack.com/services/T0/B0/X"},
			},
			config.SectionWatches: watches,
		},
		Defaults: map[string]any{rules.SectionPolicy: map[string]any{rules.PolicyKeyCooldown: "5m"}},
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
			Defaults: map[string]any{rules.SectionPolicy: map[string]any{rules.PolicyKeyCooldown: "5m"}},
		},
		Services: map[string]*config.Document{
			"customd": {
				Kind: "service",
				Name: "customd",
				Body: map[string]any{
					config.EntryKeyEnabled:   true,
					config.ServiceKeyService: "customd",
					config.SectionWatches: map[string]any{
						serviceStatusWatchName: map[string]any{config.WatchKeyCheck: map[string]any{
							checks.CheckKeyType:   checks.CheckTypeService,
							checks.CheckKeyExpect: string(servicemgr.StatusActive),
						}},
						serviceConfigWatchName: map[string]any{
							config.EntryKeyInterval: serviceConfigWatchInterval,
							config.WatchKeyCheck: map[string]any{
								checks.CheckKeyType:     checks.CheckTypeConfig,
								checks.CheckKeyPath:     []any{"/etc/customd.conf"},
								checks.CheckKeyOnChange: true,
							},
						},
					},
					config.ServiceKeyPidfile: "/run/customd.pid",
					config.EntryKeyDryRun:    true,
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
			Defaults: map[string]any{rules.SectionPolicy: map[string]any{rules.PolicyKeyCooldown: "5m"}},
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

func TestGeneratedMountsPassConfigValidation(t *testing.T) {
	body := buildMountUnit(MountCandidate{Path: "/mnt/backup"}, mountSettings{refcount: true})
	cfg := &config.Config{
		Global: config.Global{
			Raw: map[string]any{
				config.SectionWatches: map[string]any{
					"mount-mnt-backup": body,
				},
			},
			Defaults: map[string]any{rules.SectionPolicy: map[string]any{rules.PolicyKeyCooldown: "5m"}},
		},
	}
	for _, issue := range config.Validate(cfg) {
		if issue.Scope == "watch mount-mnt-backup" {
			t.Errorf("wizard-generated mount failed validation: %s", issue)
		}
	}
}
