package assist

import (
	"fmt"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/rules"
)

// uplinkAssistant generates the full monitoring set for an internet uplink
// interface (PPPoE, WAN port, LTE modem): link state, assigned address,
// default route, a bound ping and resolution through the system resolver —
// the same layering the pppd catalog service uses, as host watches for uplinks
// that are not a managed service.
type uplinkAssistant struct{}

// Name implements Assistant.
func (uplinkAssistant) Name() string { return "uplink" }

// Title implements Assistant.
func (uplinkAssistant) Title() string {
	return "Internet uplink checks (link, IP, route, ping, DNS)"
}

// Run implements Assistant.
func (uplinkAssistant) Run(p *Prompt, env Env) (res Result, err error) {
	// Translate an input-closed re-prompt abort into ErrInputClosed even when
	// Run is driven directly (the CLI also recovers at its own boundary).
	defer Recover(&err)
	ifaces, err := env.Ifaces()
	if err != nil {
		return Result{}, fmt.Errorf("list interfaces: %w", err)
	}
	cands := nonLoopbackIfaces(ifaces)
	if len(cands) == 0 {
		return Result{}, fmt.Errorf("no candidate interfaces found")
	}
	selected := chooseIfaces(p, "Which uplink interfaces do you want to monitor?", cands, env.DefaultIfaces, true)

	s := uplinkSettings{Monitoring: p.AskMonitoring("the uplink watches")}
	s.probeHost = p.Ask("Probe host to ping through the uplink", "1.1.1.1")
	s.probeName = p.Ask("Public DNS name to resolve through the uplink", "example.com")
	s.forCycles = p.AskInt("Require probe failures for how many cycles first?", 3)
	s.notifiers = chooseNotifiers(p, env)
	s.dryRun = p.AskWatchDryRun("the uplink watches", env, s.notifiers, false)

	watches := map[string]any{}
	for _, iface := range selected {
		for name, entry := range buildUplinkWatches(iface.Name, s) {
			watches[name] = entry
		}
	}
	return Result{Watches: watches, Summary: fmt.Sprintf("%d uplink watch(es)", len(watches))}, nil
}

type uplinkSettings struct {
	Monitoring // shared monitor-state + interval, applied to every uplink watch
	probeHost  string
	probeName  string
	forCycles  int
	notifiers  []string
	dryRun     bool
}

// buildUplinkWatches emits the watch set for one uplink interface: the same
// link / address / route / ping / DNS layering the pppd catalog service uses
// for its check-only service watches. The probe layers (ping, DNS) carry the
// for-cycles debounce; the local layers (link, address, route) fire immediately.
func buildUplinkWatches(iface string, s uplinkSettings) map[string]any {
	newThen := func() map[string]any {
		return watchThen(s.notifiers)
	}
	debounce := func(entry map[string]any) map[string]any {
		if s.forCycles > 0 {
			entry[rules.RuleFieldFor] = map[string]any{rules.WindowKeyCycles: s.forCycles}
		}
		return entry
	}
	watches := map[string]any{
		"uplink-" + iface: map[string]any{
			config.WatchKeyCheck: map[string]any{checks.CheckKeyType: checks.CheckTypeNet, checks.CheckKeyInterface: iface},
			config.SectionMetrics: map[string]any{
				// Alert while the link is down, and on a provider-forced
				// renumbering or reconnect (also the dynamic-DNS trigger).
				checks.NetMetricState: map[string]any{
					checks.CheckKeyExpect: checks.NetStateDown,
					config.WatchKeyThen:   newThen(),
				},
				checks.NetMetricAddress: map[string]any{
					checks.CheckKeyOn:   checks.OnModeChange,
					config.WatchKeyThen: newThen(),
				},
			},
		},
		"uplink-" + iface + "-route": map[string]any{
			config.WatchKeyCheck: map[string]any{checks.CheckKeyType: checks.CheckTypeRoute, checks.CheckKeyInterface: iface},
			config.WatchKeyThen:  newThen(),
		},
		"uplink-" + iface + "-ping": map[string]any{
			config.WatchKeyCheck: map[string]any{
				checks.CheckKeyType:      checks.CheckTypeICMP,
				checks.CheckKeyHost:      s.probeHost,
				checks.CheckKeyInterface: iface,
			},
			config.SectionMetrics: map[string]any{
				checks.NetMetricState: debounce(map[string]any{
					checks.CheckKeyExpect: checks.NetStateDown,
					config.WatchKeyThen:   newThen(),
				}),
			},
		},
		"uplink-" + iface + "-dns": debounce(map[string]any{
			config.WatchKeyCheck: map[string]any{
				checks.CheckKeyType:       "dns",
				checks.CheckKeyResolvconf: true,
				checks.CheckKeyQuery:      s.probeName,
				checks.CheckKeyExpect: map[string]any{
					"rcode":   "NOERROR",
					"answers": map[string]any{checks.CheckKeyOp: ">", checks.CheckKeyValue: 0},
				},
				checks.CheckKeyTimeout: "5s",
			},
			config.WatchKeyThen: newThen(),
		}),
	}
	for _, entry := range watches {
		if m, ok := entry.(map[string]any); ok {
			m[config.EntryKeyCategory] = watchCategoryNetwork
			s.Monitoring.apply(m)
			applyDryRun(m, s.dryRun)
		}
	}
	return watches
}
