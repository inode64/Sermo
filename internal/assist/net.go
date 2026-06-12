package assist

import (
	"fmt"

	"sermo/internal/config"
)

// netAssistant creates `net` (network interface) watches: per-interface metrics
// (link state, errors, speed) each notifying the chosen targets.
type netAssistant struct{}

func (netAssistant) Name() string  { return "net" }
func (netAssistant) Title() string { return "Network interface checks (link state, errors, speed)" }

func (netAssistant) Run(p *Prompt, env Env) (Result, error) {
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
	metrics   []string // any of: state, errors, speed
	stateDown bool     // expect:down instead of on:change
	errorsAt  int
	notifiers []string
}

func askNetSettings(p *Prompt, env Env, label string) (netSettings, error) {
	var s netSettings
	options := []string{"link up/down", "link errors", "link speed changes"}
	keys := []string{"state", "errors", "speed"}
	for _, idx := range p.MultiChoose("What do you want to monitor on "+label+"?", options) {
		s.metrics = append(s.metrics, keys[idx])
	}
	for _, m := range s.metrics {
		switch m {
		case "state":
			s.stateDown = p.Choose("For link state, alert when…", []string{"it changes (up or down)", "it goes down"}) == 1
		case "errors":
			s.errorsAt = p.AskInt("Alert when interface errors per cycle exceed", 100)
		}
	}
	s.notifiers = chooseNotifiers(p, env)
	if !config.HasEffectiveNotifyAction(s.notifiers, env.DefaultNotify) {
		return s, fmt.Errorf("a net watch needs at least one notifier; none chosen for %s", label)
	}
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
		}
	}
	return map[string]any{
		"check":   map[string]any{"type": "net", "interface": iface.Name},
		"metrics": metrics,
	}
}
