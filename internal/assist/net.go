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
	var cands []Iface
	for _, i := range ifaces {
		if !i.Loopback {
			cands = append(cands, i)
		}
	}
	if len(cands) == 0 {
		return Result{}, fmt.Errorf("no non-loopback network interfaces found")
	}
	labels := make([]string, len(cands))
	for i, c := range cands {
		st := "down"
		if c.Up {
			st = "up"
		}
		labels[i] = fmt.Sprintf("%s (%s)", c.Name, st)
	}
	sel := p.MultiChoose("Which interfaces do you want to monitor?", labels)

	var shared *netSettings
	if len(sel) > 1 && p.Confirm("Apply the same settings to all selected interfaces?", true) {
		s, err := askNetSettings(p, env, "the selected interfaces")
		if err != nil {
			return Result{}, err
		}
		shared = &s
	}

	watches := map[string]any{}
	for _, i := range sel {
		c := cands[i]
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

type netSettings struct {
	metrics    []string // any of: state, errors, speed, address
	stateDown  bool     // expect:down instead of on:change
	addrAbsent bool     // expect:absent instead of on:change
	errorsAt   int
	notifiers  []string
}

func askNetSettings(p *Prompt, env Env, label string) (netSettings, error) {
	var s netSettings
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
	// A net watch has no non-notify action, so the answer must deliver
	// somewhere; ensureNotifyAction re-asks until it does.
	s.notifiers = ensureNotifyAction(p, env, chooseNotifiers(p, env), false)
	return s, nil
}

func buildNetWatch(iface Iface, s netSettings) map[string]any {
	newThen := func() map[string]any {
		then := map[string]any{}
		if len(s.notifiers) > 0 {
			then["notify"] = s.notifiers
		}
		return then
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
			cond := map[string]any{"then": newThen()}
			if s.addrAbsent {
				cond["expect"] = "absent"
			} else {
				cond["on"] = "change"
			}
			metrics["address"] = cond
		}
	}
	return map[string]any{
		"check":   map[string]any{"type": "net", "interface": iface.Name},
		"metrics": metrics,
	}
}
