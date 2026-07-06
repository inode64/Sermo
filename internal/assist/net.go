package assist

import (
	"fmt"
)

// netAssistant creates `net` (network interface) watches: per-interface metrics
// (link state, errors, speed) each notifying the chosen targets.
type netAssistant struct{}

func (netAssistant) Name() string  { return "net" }
func (netAssistant) Title() string { return "Network interface checks (link state, errors, speed)" }

func (netAssistant) Run(p *Prompt, env Env) (res Result, err error) {
	// Translate an input-closed re-prompt abort into ErrInputClosed even when
	// Run is driven directly (the CLI also recovers at its own boundary).
	defer Recover(&err)
	ifaces, err := env.Ifaces()
	if err != nil {
		return Result{}, fmt.Errorf("list interfaces: %w", err)
	}
	cands := nonLoopbackIfaces(ifaces)
	if len(cands) == 0 {
		return Result{}, fmt.Errorf("no non-loopback network interfaces found")
	}
	selected := chooseIfaces(p, "Which interfaces do you want to monitor?", cands, env.DefaultIfaces, false)

	var shared *netSettings
	if len(selected) > 1 && p.Confirm("Apply the same settings to all selected interfaces?", true) {
		s, err := askNetSettings(p, env, "the selected interfaces")
		if err != nil {
			return Result{}, err
		}
		shared = &s
	}

	watches := map[string]any{}
	for _, c := range selected {
		s := shared
		if s == nil {
			t, err := askNetSettings(p, env, c.Name)
			if err != nil {
				return Result{}, err
			}
			s = &t
		}
		watches["net-"+c.Name] = buildNetWatch(c, *s)
	}
	return Result{Watches: watches, Summary: fmt.Sprintf("%d net watch(es)", len(watches))}, nil
}

func nonLoopbackIfaces(ifaces []Iface) []Iface {
	out := make([]Iface, 0, len(ifaces))
	for _, iface := range ifaces {
		if !iface.Loopback {
			out = append(out, iface)
		}
	}
	return out
}

func chooseIfaces(p *Prompt, question string, cands []Iface, defaultIfaces []string, allowDefault bool) []Iface {
	defaults := stringSet(defaultIfaces)
	labels := make([]string, len(cands))
	var hasActive, hasDefault bool
	for i, c := range cands {
		labels[i] = ifaceLabel(c, defaults[c.Name])
		hasActive = hasActive || c.Up
		hasDefault = hasDefault || defaults[c.Name]
	}
	var keywords []string
	if hasActive {
		keywords = append(keywords, "active")
	}
	if allowDefault && hasDefault {
		keywords = append(keywords, "default")
	}
	sel, keyword := p.MultiChooseKeyword(question, labels, keywords...)
	switch keyword {
	case "active":
		return filterIfaces(cands, func(c Iface) bool { return c.Up })
	case "default":
		return filterIfaces(cands, func(c Iface) bool { return defaults[c.Name] })
	default:
		return candidatesByIndexes(cands, sel)
	}
}

func ifaceLabel(iface Iface, defaultRoute bool) string {
	state := "down"
	if iface.Up {
		state = "up"
	}
	label := fmt.Sprintf("%s (%s)", iface.Name, state)
	if defaultRoute {
		label += ", default route"
	}
	return label
}

func filterIfaces(ifaces []Iface, keep func(Iface) bool) []Iface {
	out := make([]Iface, 0, len(ifaces))
	for _, iface := range ifaces {
		if keep(iface) {
			out = append(out, iface)
		}
	}
	return out
}

func stringSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

type netSettings struct {
	Monitoring          // shared monitor-state + interval (asked first, see docs/wizards.md)
	metrics    []string // any of: state, errors, speed, address
	stateDown  bool     // expect:down instead of on:change
	addrAbsent bool     // expect:absent instead of on:change
	errorsAt   int
	notifiers  []string
	dryRun     bool
}

func askNetSettings(p *Prompt, env Env, label string) (netSettings, error) {
	var s netSettings
	s.Monitoring = p.AskMonitoring(label)
	options := []string{"link up/down", "link errors", "link speed changes", "IP address (lost or changed)"}
	keys := []string{"state", "errors", "speed", "address"}
	for _, idx := range p.MultiChoose("What do you want to monitor on "+label+"?", options) {
		s.metrics = append(s.metrics, keys[idx])
	}
	for _, m := range s.metrics {
		switch m {
		case "state":
			s.stateDown = p.Choose("For link state, alert when…", []string{"it changes (up or down)", "it goes down"}) == 1
		case "errors":
			s.errorsAt = p.AskInt("Alert when interface errors per cycle exceed", 100)
		case "address":
			s.addrAbsent = p.Choose("For the IP address, alert when…", []string{"it changes (reconnect/renumbering)", "the interface has no address"}) == 1
		}
	}
	s.notifiers = chooseNotifiers(p, env)
	s.dryRun = p.AskWatchDryRun(label, env, s.notifiers, false)
	return s, nil
}

func buildNetWatch(iface Iface, s netSettings) map[string]any {
	newThen := func() map[string]any {
		return watchThen(s.notifiers)
	}
	metrics := map[string]any{}
	for _, m := range s.metrics {
		switch m {
		case "state":
			cond := map[string]any{"then": newThen()}
			if s.stateDown {
				cond["expect"] = "down"
			} else {
				cond["on"] = "change"
			}
			metrics["state"] = cond
		case "errors":
			metrics["errors"] = map[string]any{
				"delta": map[string]any{"op": ">", "value": s.errorsAt},
				"then":  newThen(),
			}
		case "speed":
			metrics["speed"] = map[string]any{"on": "change", "then": newThen()}
		case "address":
			if s.addrAbsent && !iface.HasAddress {
				continue
			}
			cond := map[string]any{"then": newThen()}
			if s.addrAbsent {
				cond["expect"] = "absent"
			} else {
				cond["on"] = "change"
			}
			metrics["address"] = cond
		}
	}
	entry := map[string]any{
		"category": "network",
		"check":    map[string]any{"type": "net", "interface": iface.Name},
		"metrics":  metrics,
	}
	s.Monitoring.apply(entry)
	applyDryRun(entry, s.dryRun)
	return entry
}
