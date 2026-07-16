package assist

import (
	"strings"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/rules"
)

func TestUplinkAssistant(t *testing.T) {
	// Select eth0 (lo filtered out); monitor enabled, inherit interval; accept the
	// probe-host/name/cycles defaults (empty lines); notifier ops-email.
	script := strings.Join([]string{"1", "1", "", "", "", "", "1", "y"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := uplinkAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, name := range []string{"uplink-eth0", "uplink-eth0-route", "uplink-eth0-ping", "uplink-eth0-dns"} {
		entry, ok := res.Watches[name].(map[string]any)
		if !ok {
			t.Fatalf("missing watch %s in %v", name, res.Watches)
		}
		if entry[config.EntryKeyCategory] != config.WatchCategoryNetwork {
			t.Fatalf("%s category = %v, want network", name, entry[config.EntryKeyCategory])
		}
	}

	link := res.Watches["uplink-eth0"].(map[string]any)
	if link[config.EntryKeyMonitor] != config.MonitorEnabled {
		t.Fatalf("monitor = %v, want enabled (applied to every uplink watch)", link[config.EntryKeyMonitor])
	}
	metrics := link[config.SectionMetrics].(map[string]any)
	if metrics[checks.NetMetricState].(map[string]any)[checks.CheckKeyExpect] != checks.NetStateDown {
		t.Fatalf("state = %v, want expect down", metrics[checks.NetMetricState])
	}
	if metrics[checks.NetMetricAddress].(map[string]any)[checks.CheckKeyOn] != checks.OnModeChange {
		t.Fatalf("address = %v, want on change", metrics[checks.NetMetricAddress])
	}

	route := res.Watches["uplink-eth0-route"].(map[string]any)
	if route[config.WatchKeyCheck].(map[string]any)[checks.CheckKeyInterface] != "eth0" {
		t.Fatalf("route check = %v", route[config.WatchKeyCheck])
	}
	if _, debounced := route[rules.RuleFieldFor]; debounced {
		t.Fatalf("the route layer must fire immediately, got %v", route)
	}

	ping := res.Watches["uplink-eth0-ping"].(map[string]any)
	state := ping[config.SectionMetrics].(map[string]any)[checks.NetMetricState].(map[string]any)
	if state[rules.RuleFieldFor].(map[string]any)[rules.WindowKeyCycles] != uplinkDefaultForCycles {
		t.Fatalf("ping state = %v, want the default 3-cycle debounce", state)
	}
	if notify := state[config.WatchKeyThen].(map[string]any)[rules.RuleFieldNotify].([]string); len(notify) != 1 || notify[0] != "ops-email" {
		t.Fatalf("ping notify = %v, want [ops-email]", notify)
	}
	if ping[config.EntryKeyDryRun] != true {
		t.Fatalf("ping dry_run = %v, want true", ping[config.EntryKeyDryRun])
	}

	dns := res.Watches["uplink-eth0-dns"].(map[string]any)
	check := dns[config.WatchKeyCheck].(map[string]any)
	if check[checks.CheckKeyResolvconf] != true || check[checks.CheckKeyQuery] != uplinkDefaultProbeName {
		t.Fatalf("dns check = %v", check)
	}
	if _, bound := check[checks.CheckKeyInterface]; bound {
		t.Fatalf("dns check must use the system resolver without an interface pin: %v", check)
	}
	if dns[rules.RuleFieldFor].(map[string]any)[rules.WindowKeyCycles] != uplinkDefaultForCycles {
		t.Fatalf("dns = %v, want the 3-cycle debounce", dns)
	}
}

func TestUplinkAssistantDefaultRouteKeyword(t *testing.T) {
	env := testEnv()
	env.Ifaces = func() ([]Iface, error) {
		return []Iface{
			{Name: "eth0", Up: true},
			{Name: "wg0", Up: true},
			{Name: "lo", Up: true, Loopback: true},
		}, nil
	}
	env.DefaultIfaces = []string{"wg0"}
	res, _ := runAssistant(t, uplinkAssistant{}, env, config.SelectionKeywordDefault, "1", "", "", "", "", "1", "y")
	if _, ok := res.Watches["uplink-wg0-route"]; !ok {
		t.Fatalf("default route interface was not selected: %v", res.Watches)
	}
	if _, ok := res.Watches["uplink-eth0-route"]; ok {
		t.Fatalf("non-default interface should not be selected: %v", res.Watches)
	}
}

func TestUplinkAssistantInheritsDefaultNotify(t *testing.T) {
	// Custom probe host and name; 'default' inherits the global notify, so
	// every generated then block omits notify.
	script := strings.Join([]string{"1", "1", "", "8.8.8.8", "cloudflare.com", "2", config.NotifyKeywordDefault, "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := uplinkAssistant{}.Run(p, testEnvWithDefaultNotify())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	dns := res.Watches["uplink-eth0-dns"].(map[string]any)
	if dns[config.WatchKeyCheck].(map[string]any)[checks.CheckKeyQuery] != "cloudflare.com" {
		t.Fatalf("dns check = %v, want the custom probe name", dns[config.WatchKeyCheck])
	}
	if _, hasNotify := dns[config.WatchKeyThen].(map[string]any)[rules.RuleFieldNotify]; hasNotify {
		t.Fatalf("notify should be omitted to inherit the global default: %v", dns[config.WatchKeyThen])
	}
	ping := res.Watches["uplink-eth0-ping"].(map[string]any)
	if ping[config.WatchKeyCheck].(map[string]any)[checks.CheckKeyHost] != "8.8.8.8" {
		t.Fatalf("ping check = %v, want the custom probe host", ping[config.WatchKeyCheck])
	}
}
