package assist

import (
	"strings"
	"testing"
)

func TestNetAssistantStateAndErrors(t *testing.T) {
	// Select iface 1 (eth0; lo is filtered out); monitor enabled, interval 10s;
	// monitor state+errors; state on any change; errors threshold 100; ops-email.
	script := strings.Join([]string{
		"1",   // MultiChoose interfaces -> eth0
		"1",   // monitor state: enabled
		"10s", // interval
		"1,2", // metrics: link up/down + link errors
		"1",   // state: any change
		"100", // errors threshold
		"1",   // notifier ops-email
		"y",   // dry-run actions first
	}, "\n") + "\n"

	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := netAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry, ok := res.Watches["net-eth0"].(map[string]any)
	if !ok {
		t.Fatalf("expected watch net-eth0, got %v", res.Watches)
	}
	if entry["check"].(map[string]any)["interface"] != "eth0" {
		t.Fatalf("check = %v", entry["check"])
	}
	if entry["monitor"] != "enabled" || entry["interval"] != "10s" {
		t.Fatalf("monitor/interval = %v / %v, want enabled / 10s", entry["monitor"], entry["interval"])
	}
	metrics := entry["metrics"].(map[string]any)
	state := metrics["state"].(map[string]any)
	if state["on"] != "change" {
		t.Fatalf("state = %v, want on:change", state)
	}
	if state["then"].(map[string]any)["notify"].([]string)[0] != "ops-email" {
		t.Fatalf("state.then = %v", state["then"])
	}
	if entry["dry_run"] != true {
		t.Fatalf("dry_run = %v, want true", entry["dry_run"])
	}
	errs := metrics["errors"].(map[string]any)
	delta := errs["delta"].(map[string]any)
	if delta["op"] != ">" || delta["value"] != 100 {
		t.Fatalf("errors.delta = %v", delta)
	}
	if _, hasSpeed := metrics["speed"]; hasSpeed {
		t.Fatalf("speed must not be present: %v", metrics)
	}
}

func TestNetAssistantActiveKeyword(t *testing.T) {
	env := testEnv()
	env.Ifaces = func() ([]Iface, error) {
		return []Iface{
			{Name: "dummy0", Up: false},
			{Name: "eth0", Up: true},
			{Name: "wg0", Up: true},
			{Name: "lo", Up: true, Loopback: true},
		}, nil
	}
	script := strings.Join([]string{
		"active", // only eth0 and wg0
		"y",      // shared settings
		"1",      // monitor enabled
		"",       // interval inherit
		"1",      // link state
		"1",      // on any change
		"1",      // ops-email
		"y",      // dry-run
	}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := netAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := res.Watches["net-eth0"]; !ok {
		t.Fatalf("net-eth0 missing from %v", res.Watches)
	}
	if _, ok := res.Watches["net-wg0"]; !ok {
		t.Fatalf("net-wg0 missing from %v", res.Watches)
	}
	if _, ok := res.Watches["net-dummy0"]; ok {
		t.Fatalf("down interface should not be selected by active keyword: %v", res.Watches)
	}
}

func TestNetAssistantStateDownOnly(t *testing.T) {
	// Select eth0; monitor enabled, inherit interval; only state; "only when
	// down"; notifier team-slack.
	script := strings.Join([]string{"1", "1", "", "1", "2", "2", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := netAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["net-eth0"].(map[string]any)
	if _, hasInterval := entry["interval"]; hasInterval {
		t.Fatalf("blank interval must be omitted to inherit the global: %v", entry)
	}
	state := entry["metrics"].(map[string]any)["state"].(map[string]any)
	if state["expect"] != "down" {
		t.Fatalf("state = %v, want expect:down", state)
	}
}

func TestNetAssistantInheritsGlobalNotify(t *testing.T) {
	// Select eth0; monitor enabled, inherit interval; only state; any change;
	// inherit global notify.
	script := strings.Join([]string{"1", "1", "", "1", "1", "default", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := netAssistant{}.Run(p, testEnvWithDefaultNotify())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	state := res.Watches["net-eth0"].(map[string]any)["metrics"].(map[string]any)["state"].(map[string]any)
	then := state["then"].(map[string]any)
	if _, hasNotify := then["notify"]; hasNotify {
		t.Fatalf("notify should be omitted to inherit global default: %v", then)
	}
}

func TestNetAssistantDefaultWithoutGlobalMonitorOnly(t *testing.T) {
	// Choosing 'default' with no global notify configured is ALWAYS accepted: it
	// degrades to a monitor-only watch (notify [none]) with an explanatory line,
	// instead of re-asking or aborting (the old behavior).
	for _, tc := range []struct {
		name string
		env  Env
	}{
		{"no notifiers at all", func() Env { e := testEnv(); e.Notifiers = nil; return e }()},
		{"notifiers but no global default", testEnv()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := strings.Join([]string{"1", "1", "", "1", "1", "default"}, "\n") + "\n"
			var out strings.Builder
			p := NewPrompt(strings.NewReader(script), &out)
			res, err := netAssistant{}.Run(p, tc.env)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			then := res.Watches["net-eth0"].(map[string]any)["metrics"].(map[string]any)["state"].(map[string]any)["then"].(map[string]any)
			notify := then["notify"].([]string)
			if len(notify) != 1 || notify[0] != "none" {
				t.Fatalf("notify = %v, want [none] (monitor-only)", notify)
			}
			if !strings.Contains(out.String(), "monitor-only") {
				t.Fatalf("expected the monitor-only note, got %q", out.String())
			}
		})
	}
}

func TestNetAssistantNotifyByName(t *testing.T) {
	// The shared all/none/default vocabulary works in the net wizard too:
	// notifiers can be picked by name, and "default" inherits the global default.
	t.Run("notifier by name", func(t *testing.T) {
		script := strings.Join([]string{"1", "1", "", "1", "1", "team-slack", "n"}, "\n") + "\n"
		p := NewPrompt(strings.NewReader(script), &strings.Builder{})
		res, err := netAssistant{}.Run(p, testEnv())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		then := res.Watches["net-eth0"].(map[string]any)["metrics"].(map[string]any)["state"].(map[string]any)["then"].(map[string]any)
		notify := then["notify"].([]string)
		if len(notify) != 1 || notify[0] != "team-slack" {
			t.Fatalf("notify = %v, want [team-slack]", notify)
		}
	})

	t.Run("default by name", func(t *testing.T) {
		script := strings.Join([]string{"1", "1", "", "1", "1", "default", "n"}, "\n") + "\n"
		p := NewPrompt(strings.NewReader(script), &strings.Builder{})
		res, err := netAssistant{}.Run(p, testEnvWithDefaultNotify())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		then := res.Watches["net-eth0"].(map[string]any)["metrics"].(map[string]any)["state"].(map[string]any)["then"].(map[string]any)
		if _, hasNotify := then["notify"]; hasNotify {
			t.Fatalf("'default' should omit notify to inherit the global default: %v", then)
		}
	})
}

func TestNetAssistantNotifyNoneMonitorOnly(t *testing.T) {
	// Select eth0; monitor enabled, inherit interval; only state; any change;
	// explicit none: the reserved opt-out generates a monitor-only watch.
	script := strings.Join([]string{"1", "1", "", "1", "1", "none"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := netAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	then := res.Watches["net-eth0"].(map[string]any)["metrics"].(map[string]any)["state"].(map[string]any)["then"].(map[string]any)
	notify := then["notify"].([]string)
	if len(notify) != 1 || notify[0] != "none" {
		t.Fatalf("notify = %v, want [none]", notify)
	}
}

func TestBuildNetWatchSkipsAbsentAddressMetricWhenAlreadyAddressless(t *testing.T) {
	settings := netSettings{metrics: []string{"address"}, addrAbsent: true, notifiers: []string{"none"}}
	without := buildNetWatch(Iface{Name: "eth0", HasAddress: false}, settings)
	if _, ok := without["metrics"].(map[string]any)["address"]; ok {
		t.Fatalf("address metric should be omitted for an already addressless interface: %v", without)
	}
	with := buildNetWatch(Iface{Name: "wg0", HasAddress: true}, settings)
	address, ok := with["metrics"].(map[string]any)["address"].(map[string]any)
	if !ok || address["expect"] != "absent" {
		t.Fatalf("address metric = %v, want expect:absent", with["metrics"])
	}
}
