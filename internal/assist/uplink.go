package assist

import "fmt"

// uplinkAssistant generates the full monitoring set for an internet uplink
// interface (PPPoE, WAN port, LTE modem): link state, assigned address,
// default route, a bound ping and resolution through the system resolver —
// the same layering the pppd catalog daemon uses, as host watches for uplinks
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
// link / address / route / ping / DNS layering the pppd catalog daemon uses
// for its service checks. The probe layers (ping, DNS) carry the for-cycles
// debounce; the local layers (link, address, route) fire immediately.
func buildUplinkWatches(iface string, s uplinkSettings) map[string]any {
	newThen := func() map[string]any {
		then := map[string]any{}
		if len(s.notifiers) > 0 {
			then["notify"] = s.notifiers
		}
		applyDryRun(then, s.dryRun)
		return then
	}
	debounce := func(entry map[string]any) map[string]any {
		if s.forCycles > 0 {
			entry["for"] = map[string]any{"cycles": s.forCycles}
		}
		return entry
	}
	watches := map[string]any{
		"uplink-" + iface: map[string]any{
			"check": map[string]any{"type": "net", "interface": iface},
			"metrics": map[string]any{
				// Alert while the link is down, and on a provider-forced
				// renumbering or reconnect (also the dynamic-DNS trigger).
				"state":   map[string]any{"expect": "down", "then": newThen()},
				"address": map[string]any{"on": "change", "then": newThen()},
			},
		},
		"uplink-" + iface + "-route": map[string]any{
			"check": map[string]any{"type": "route", "interface": iface},
			"then":  newThen(),
		},
		"uplink-" + iface + "-ping": map[string]any{
			"check": map[string]any{"type": "icmp", "host": s.probeHost, "interface": iface},
			"metrics": map[string]any{
				"state": debounce(map[string]any{"expect": "down", "then": newThen()}),
			},
		},
		"uplink-" + iface + "-dns": debounce(map[string]any{
			"check": map[string]any{
				"type": "dns", "resolvconf": true, "query": s.probeName, "interface": iface,
				"expect":  map[string]any{"rcode": "NOERROR", "answers": map[string]any{"op": ">", "value": 0}},
				"timeout": "5s",
			},
			"then": newThen(),
		}),
	}
	for _, entry := range watches {
		if m, ok := entry.(map[string]any); ok {
			s.Monitoring.apply(m)
		}
	}
	return watches
}
