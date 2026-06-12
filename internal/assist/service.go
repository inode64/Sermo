package assist

import (
	"fmt"
	"sort"
	"strconv"
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

func (serviceAssistant) Run(p *Prompt, env Env) (Result, error) {
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

	services := map[string]any{}
	for _, idx := range sel {
		name, body := askService(p, env, cands[idx])
		if name != "" {
			services[name] = body
		}
	}

	// Allow enabling a catalog daemon that was not auto-detected (entered by name).
	for p.Confirm("Add another service by catalog daemon name (not detected)?", false) {
		daemon := p.AskNonEmpty("Catalog daemon name (e.g. nginx, mariadb)")
		name, body := askService(p, env, DaemonCandidate{Name: daemon, Title: daemon})
		if name != "" {
			services[name] = body
		}
	}

	if len(services) == 0 {
		return Result{}, nil
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

// askService asks for the instance name and optional port override for one
// candidate, returning the service name and its body (or "" to skip).
func askService(p *Prompt, env Env, c DaemonCandidate) (string, map[string]any) {
	name := p.Ask("Service name for "+c.Name+"?", c.Name)
	if name == "" {
		name = c.Name
	}
	if _, exists := env.ServiceNames[name]; exists {
		p.printf("  %q is already configured; skipping.\n", name)
		return "", nil
	}
	body := map[string]any{"uses": c.Name, "enabled": true}
	if c.Port > 0 {
		ans := p.Ask(fmt.Sprintf("Port for %s?", name), strconv.Itoa(c.Port))
		if n, err := strconv.Atoi(strings.TrimSpace(ans)); err == nil && n > 0 && n != c.Port {
			body["variables"] = map[string]any{"port": n}
		}
	}
	return name, body
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
