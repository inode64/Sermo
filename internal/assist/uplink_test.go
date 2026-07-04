package assist

import (
	"strings"
	"testing"
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
		if entry["category"] != "network" {
			t.Fatalf("%s category = %v, want network", name, entry["category"])
		}
	}

	link := res.Watches["uplink-eth0"].(map[string]any)
	if link["monitor"] != "enabled" {
		t.Fatalf("monitor = %v, want enabled (applied to every uplink watch)", link["monitor"])
	}
	metrics := link["metrics"].(map[string]any)
	if metrics["state"].(map[string]any)["expect"] != "down" {
		t.Fatalf("state = %v, want expect down", metrics["state"])
	}
	if metrics["address"].(map[string]any)["on"] != "change" {
		t.Fatalf("address = %v, want on change", metrics["address"])
	}

	route := res.Watches["uplink-eth0-route"].(map[string]any)
	if route["check"].(map[string]any)["interface"] != "eth0" {
		t.Fatalf("route check = %v", route["check"])
	}
	if _, debounced := route["for"]; debounced {
		t.Fatalf("the route layer must fire immediately, got %v", route)
	}

	ping := res.Watches["uplink-eth0-ping"].(map[string]any)
	state := ping["metrics"].(map[string]any)["state"].(map[string]any)
	if state["for"].(map[string]any)["cycles"] != 3 {
		t.Fatalf("ping state = %v, want the default 3-cycle debounce", state)
	}
	if notify := state["then"].(map[string]any)["notify"].([]string); len(notify) != 1 || notify[0] != "ops-email" {
		t.Fatalf("ping notify = %v, want [ops-email]", notify)
	}
	if ping["dry_run"] != true {
		t.Fatalf("ping dry_run = %v, want true", ping["dry_run"])
	}

	dns := res.Watches["uplink-eth0-dns"].(map[string]any)
	check := dns["check"].(map[string]any)
	if check["resolvconf"] != true || check["query"] != "example.com" {
		t.Fatalf("dns check = %v", check)
	}
	if _, bound := check["interface"]; bound {
		t.Fatalf("dns check must use the system resolver without an interface pin: %v", check)
	}
	if dns["for"].(map[string]any)["cycles"] != 3 {
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
	script := strings.Join([]string{"default", "1", "", "", "", "", "1", "y"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := uplinkAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
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
	script := strings.Join([]string{"1", "1", "", "8.8.8.8", "cloudflare.com", "2", "default", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := uplinkAssistant{}.Run(p, testEnvWithDefaultNotify())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	dns := res.Watches["uplink-eth0-dns"].(map[string]any)
	if dns["check"].(map[string]any)["query"] != "cloudflare.com" {
		t.Fatalf("dns check = %v, want the custom probe name", dns["check"])
	}
	if _, hasNotify := dns["then"].(map[string]any)["notify"]; hasNotify {
		t.Fatalf("notify should be omitted to inherit the global default: %v", dns["then"])
	}
	ping := res.Watches["uplink-eth0-ping"].(map[string]any)
	if ping["check"].(map[string]any)["host"] != "8.8.8.8" {
		t.Fatalf("ping check = %v, want the custom probe host", ping["check"])
	}
}
