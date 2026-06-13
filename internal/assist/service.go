package assist

import (
	"fmt"
	"sort"
	"strings"
)

// serviceAssistant enables a catalog daemon as a monitored service. It detects
// the active init system (systemd/openrc) and each candidate's resolved unit,
// default port (and whether it is listening) and config locations, then writes a
// `kind: service` file that `uses:` the catalog daemon.
type serviceAssistant struct{}

func (serviceAssistant) Name() string { return "service" }
func (serviceAssistant) Title() string {
	return "Monitor a system service (apache, nginx, mysql, …)"
}

func (serviceAssistant) Run(p *Prompt, env Env) (res Result, err error) {
	// Translate an input-closed re-prompt abort into ErrInputClosed even when
	// Run is driven directly (the CLI also recovers at its own boundary).
	defer Recover(&err)
	if env.Daemons == nil {
		return Result{}, fmt.Errorf("service detection is unavailable")
	}
	cands, err := env.Daemons()
	if err != nil {
		return Result{}, fmt.Errorf("detect installed services: %w", err)
	}
	if env.Backend != "" {
		p.printf("Detected init system: %s\n\n", env.Backend)
	}
	if len(cands) == 0 {
		return Result{}, fmt.Errorf("no catalog services were detected as installed on this host")
	}

	labels := make([]string, len(cands))
	for i, c := range cands {
		labels[i] = serviceLabel(c)
	}
	sel := p.MultiChoose("Which services do you want to monitor?", labels)

	// Per-service properties first (these legitimately differ per service: port,
	// pidfile/exe). The shared monitor-state + interval come after, batched.
	type pending struct {
		name string
		body map[string]any
	}
	var items []pending
	for _, idx := range sel {
		name, body := askServiceProps(p, env, cands[idx])
		if name != "" {
			items = append(items, pending{name, body})
		}
	}
	if len(items) == 0 {
		return Result{}, nil
	}

	// Batch: when more than one service was selected, offer to answer the shared
	// monitoring questions once and apply them to all (docs/wizards.md step 4).
	var shared *Monitoring
	if len(items) > 1 && p.Confirm("Apply the same monitor state and interval to all selected services?", true) {
		m := p.AskMonitoring("all selected services")
		shared = &m
	}

	services := map[string]any{}
	for _, it := range items {
		m := shared
		if m == nil {
			mm := p.AskMonitoring(it.name)
			m = &mm
		}
		m.apply(it.body)
		services[it.name] = it.body
	}

	names := make([]string, 0, len(services))
	for n := range services {
		names = append(names, n)
	}
	sort.Strings(names)
	return Result{
		Services: services,
		Summary:  fmt.Sprintf("%d service(s): %s", len(names), strings.Join(names, ", ")),
	}, nil
}

// askServiceProps asks the per-service properties for one detected candidate —
// optional port override and the PID source — returning the service name (= the
// candidate name; the wizard never invents names) and its body, or "" to skip a
// name already configured. The PID question is prefilled from the init-script
// analysis: a pidfile path writes `pidfile:`, and if there is none, an exe
// derived from the unit offers a `command_match` selector.
func askServiceProps(p *Prompt, env Env, c DaemonCandidate) (string, map[string]any) {
	if _, exists := env.ServiceNames[c.Name]; exists {
		p.printf("  %q is already configured; skipping.\n", c.Name)
		return "", nil
	}
	body := map[string]any{"uses": c.Name, "enabled": true}
	if c.Port > 0 {
		if n := p.AskInt(fmt.Sprintf("Port for %s?", c.Name), c.Port); n > 0 && n != c.Port {
			body["variables"] = map[string]any{"port": n}
		}
	}
	if pidfile := strings.TrimSpace(p.Ask("Pidfile path for "+c.Name+" (blank to skip)", c.Pidfile)); pidfile != "" {
		body["pidfile"] = pidfile
	} else if c.Exe != "" && p.Confirm("No pidfile — match "+c.Name+" by its executable "+c.Exe+"?", true) {
		body["processes"] = map[string]any{"main": map[string]any{"type": "command_match", "exe": c.Exe}}
	}
	return c.Name, body
}

// serviceLabel renders the candidate's detected facts for the selection menu.
func serviceLabel(c DaemonCandidate) string {
	parts := []string{c.Title}
	if c.Unit != "" {
		parts = append(parts, "unit: "+c.Unit)
	}
	if c.Port > 0 {
		port := fmt.Sprintf("port %d", c.Port)
		if c.PortListening {
			port += " (listening)"
		}
		parts = append(parts, port)
	}
	if len(c.ConfigPaths) > 0 {
		parts = append(parts, "config: "+c.ConfigPaths[0])
	}
	if !c.UnitPresent {
		parts = append(parts, "unit not found")
	}
	return strings.Join(parts, " · ")
}
