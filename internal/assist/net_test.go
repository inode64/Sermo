package assist

import (
	"strings"
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/rules"
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
	entry, ok := res.Watches[netWatchPrefix+"eth0"].(map[string]any)
	if !ok {
		t.Fatalf("expected watch net-eth0, got %v", res.Watches)
	}
	if entry[config.WatchKeyCheck].(map[string]any)[checks.CheckKeyInterface] != "eth0" {
		t.Fatalf("check = %v", entry[config.WatchKeyCheck])
	}
	if entry[config.EntryKeyCategory] != config.WatchCategoryNetwork {
		t.Fatalf("category = %v, want network", entry[config.EntryKeyCategory])
	}
	if entry[config.EntryKeyMonitor] != config.MonitorEnabled || entry[config.EntryKeyInterval] != "10s" {
		t.Fatalf("monitor/interval = %v / %v, want enabled / 10s", entry[config.EntryKeyMonitor], entry[config.EntryKeyInterval])
	}
	metrics := entry[config.SectionMetrics].(map[string]any)
	state := metrics[checks.NetMetricState].(map[string]any)
	if state[checks.CheckKeyOn] != checks.OnModeChange {
		t.Fatalf("state = %v, want on:change", state)
	}
	if state[config.WatchKeyThen].(map[string]any)[rules.RuleFieldNotify].([]string)[0] != "ops-email" {
		t.Fatalf("state.then = %v", state[config.WatchKeyThen])
	}
	if entry[config.EntryKeyDryRun] != true {
		t.Fatalf("dry_run = %v, want true", entry[config.EntryKeyDryRun])
	}
	errs := metrics[checks.NetMetricErrors].(map[string]any)
	delta := errs[checks.CheckKeyDelta].(map[string]any)
	if delta[checks.CheckKeyOp] != cfgval.CompareOpGreater || delta[checks.CheckKeyValue] != netDefaultErrorDelta {
		t.Fatalf("errors.delta = %v", delta)
	}
	if _, hasSpeed := metrics[checks.NetMetricSpeed]; hasSpeed {
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
		netKeywordActive, // only eth0 and wg0
		"y",              // shared settings
		"1",              // monitor enabled
		"",               // interval inherit
		"1",              // link state
		"1",              // on any change
		"1",              // ops-email
		"y",              // dry-run
	}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := netAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := res.Watches[netWatchPrefix+"eth0"]; !ok {
		t.Fatalf("net-eth0 missing from %v", res.Watches)
	}
	if _, ok := res.Watches[netWatchPrefix+"wg0"]; !ok {
		t.Fatalf("net-wg0 missing from %v", res.Watches)
	}
	if _, ok := res.Watches[netWatchPrefix+"dummy0"]; ok {
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
	entry := res.Watches[netWatchPrefix+"eth0"].(map[string]any)
	if _, hasInterval := entry[config.EntryKeyInterval]; hasInterval {
		t.Fatalf("blank interval must be omitted to inherit the global: %v", entry)
	}
	state := entry[config.SectionMetrics].(map[string]any)[checks.NetMetricState].(map[string]any)
	if state[checks.CheckKeyExpect] != checks.NetStateDown {
		t.Fatalf("state = %v, want expect:down", state)
	}
}

func TestNetAssistantInheritsGlobalNotify(t *testing.T) {
	// Select eth0; monitor enabled, inherit interval; only state; any change;
	// inherit global notify.
	script := strings.Join([]string{"1", "1", "", "1", "1", config.NotifyKeywordDefault, "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := netAssistant{}.Run(p, testEnvWithDefaultNotify())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	state := res.Watches[netWatchPrefix+"eth0"].(map[string]any)[config.SectionMetrics].(map[string]any)[checks.NetMetricState].(map[string]any)
	then := state[config.WatchKeyThen].(map[string]any)
	if _, hasNotify := then[rules.RuleFieldNotify]; hasNotify {
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
			script := strings.Join([]string{"1", "1", "", "1", "1", config.NotifyKeywordDefault}, "\n") + "\n"
			var out strings.Builder
			p := NewPrompt(strings.NewReader(script), &out)
			res, err := netAssistant{}.Run(p, tc.env)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			then := res.Watches[netWatchPrefix+"eth0"].(map[string]any)[config.SectionMetrics].(map[string]any)[checks.NetMetricState].(map[string]any)[config.WatchKeyThen].(map[string]any)
			notify := then[rules.RuleFieldNotify].([]string)
			if len(notify) != 1 || notify[0] != config.NotifyNone {
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
		then := res.Watches[netWatchPrefix+"eth0"].(map[string]any)[config.SectionMetrics].(map[string]any)[checks.NetMetricState].(map[string]any)[config.WatchKeyThen].(map[string]any)
		notify := then[rules.RuleFieldNotify].([]string)
		if len(notify) != 1 || notify[0] != "team-slack" {
			t.Fatalf("notify = %v, want [team-slack]", notify)
		}
	})

	t.Run("default by name", func(t *testing.T) {
		script := strings.Join([]string{"1", "1", "", "1", "1", config.NotifyKeywordDefault, "n"}, "\n") + "\n"
		p := NewPrompt(strings.NewReader(script), &strings.Builder{})
		res, err := netAssistant{}.Run(p, testEnvWithDefaultNotify())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		then := res.Watches[netWatchPrefix+"eth0"].(map[string]any)[config.SectionMetrics].(map[string]any)[checks.NetMetricState].(map[string]any)[config.WatchKeyThen].(map[string]any)
		if _, hasNotify := then[rules.RuleFieldNotify]; hasNotify {
			t.Fatalf("'default' should omit notify to inherit the global default: %v", then)
		}
	})
}

func TestNetAssistantNotifyNoneMonitorOnly(t *testing.T) {
	// Select eth0; monitor enabled, inherit interval; only state; any change;
	// explicit none: the reserved opt-out generates a monitor-only watch.
	script := strings.Join([]string{"1", "1", "", "1", "1", config.NotifyNone}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := netAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	then := res.Watches[netWatchPrefix+"eth0"].(map[string]any)[config.SectionMetrics].(map[string]any)[checks.NetMetricState].(map[string]any)[config.WatchKeyThen].(map[string]any)
	notify := then[rules.RuleFieldNotify].([]string)
	if len(notify) != 1 || notify[0] != config.NotifyNone {
		t.Fatalf("notify = %v, want [none]", notify)
	}
}

func TestBuildNetWatchSkipsAbsentAddressMetricWhenAlreadyAddressless(t *testing.T) {
	settings := netSettings{metrics: []string{checks.NetMetricAddress}, addrAbsent: true, notifiers: []string{config.NotifyNone}}
	without := buildNetWatch(Iface{Name: "eth0", HasAddress: false}, settings)
	if _, ok := without[config.SectionMetrics].(map[string]any)[checks.NetMetricAddress]; ok {
		t.Fatalf("address metric should be omitted for an already addressless interface: %v", without)
	}
	with := buildNetWatch(Iface{Name: "wg0", HasAddress: true}, settings)
	address, ok := with[config.SectionMetrics].(map[string]any)[checks.NetMetricAddress].(map[string]any)
	if !ok || address[checks.CheckKeyExpect] != checks.NetAddrAbsent {
		t.Fatalf("address metric = %v, want expect:absent", with[config.SectionMetrics])
	}
}
