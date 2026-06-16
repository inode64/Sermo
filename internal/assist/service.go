package assist

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// serviceAssistant enables a catalog daemon as a monitored service. It detects
// the active init system (systemd/openrc) and each candidate's resolved unit,
// default port (and whether it is listening) and config locations, then writes a
// `kind: service` file that `uses:` the catalog daemon.
type serviceAssistant struct{}

const serviceConfigCheckInterval = "60m"

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
	activeCatalog, generic := splitServiceCandidates(cands)
	if len(activeCatalog) == 0 && len(generic) == 0 {
		return Result{}, fmt.Errorf("no active services were detected on this host")
	}

	// Per-service properties first. Catalog services inherit PID/process
	// detection from their daemon profile, while generic services still need a
	// local PID source because they have no catalog owner. The shared
	// monitor-state + interval + shadow mode come after, batched.
	type pending struct {
		name string
		body map[string]any
	}
	services := map[string]any{}
	addGroup := func(cands []DaemonCandidate, question string, allowNone bool) {
		if len(cands) == 0 {
			return
		}
		selected := chooseServices(p, question, cands, allowNone)
		if len(selected) == 0 {
			return
		}
		reviewPorts := len(selected) == 1
		if len(selected) > 1 && groupHasPortDefaults(selected) {
			reviewPorts = p.Confirm("Review per-service port overrides?", false)
		}
		var items []pending
		for _, c := range selected {
			name, body := askServiceProps(p, env, c, reviewPorts)
			if name != "" {
				items = append(items, pending{name, body})
			}
		}
		if len(items) == 0 {
			return
		}

		// Batch: when more than one service was selected, offer to answer the shared
		// service questions once and apply them to all (docs/wizards.md step 4).
		var shared *serviceSettings
		if len(items) > 1 && p.Confirm("Apply the same monitor state, interval and shadow mode to all selected services?", true) {
			s := askServiceSettings(p, "all selected services")
			shared = &s
		}

		for _, it := range items {
			s := shared
			if s == nil {
				ss := askServiceSettings(p, it.name)
				s = &ss
			}
			s.apply(it.body)
			services[it.name] = it.body
		}
	}

	if len(activeCatalog) > 0 {
		addGroup(activeCatalog, "Which active catalog services do you want to monitor?", false)
	} else {
		p.printf("No active catalog services were detected.\n\n")
	}
	if len(generic) > 0 && p.Confirm("Review active services without catalog profiles?", false) {
		addGroup(generic, "Which uncataloged active services do you want to monitor?", true)
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

type serviceSettings struct {
	Monitoring
	Shadow bool
}

func askServiceSettings(p *Prompt, label string) serviceSettings {
	return serviceSettings{
		Monitoring: p.AskMonitoring(label),
		Shadow:     p.Confirm("Start "+label+" remediation in shadow mode (evaluate rules but skip service actions)?", false),
	}
}

func (s serviceSettings) apply(body map[string]any) {
	s.Monitoring.apply(body)
	if s.Shadow {
		body["remediation"] = map[string]any{"shadow": true}
	}
}

func splitServiceCandidates(cands []DaemonCandidate) (activeCatalog, generic []DaemonCandidate) {
	for _, c := range cands {
		if !serviceCandidateActive(c) {
			continue
		}
		if c.Generic {
			generic = append(generic, c)
			continue
		}
		activeCatalog = append(activeCatalog, c)
	}
	return activeCatalog, generic
}

func serviceCandidateActive(c DaemonCandidate) bool {
	return c.Status == "active"
}

func chooseServices(p *Prompt, question string, cands []DaemonCandidate, allowNone bool) []DaemonCandidate {
	labels := make([]string, len(cands))
	for i, c := range cands {
		labels[i] = serviceLabel(c)
	}
	var sel []int
	if allowNone {
		var keyword string
		sel, keyword = p.MultiChooseKeyword(question, labels, "none")
		if keyword == "none" {
			return nil
		}
	} else {
		sel = p.MultiChoose(question, labels)
	}
	out := make([]DaemonCandidate, 0, len(sel))
	for _, idx := range sel {
		out = append(out, cands[idx])
	}
	return out
}

func groupHasPortDefaults(cands []DaemonCandidate) bool {
	for _, c := range cands {
		if c.Port > 0 {
			return true
		}
	}
	return false
}

// askServiceProps asks the per-service properties for one detected candidate,
// returning the service name (= the candidate name; the wizard never invents
// names) and its body, or "" to skip a name already configured. Catalog
// services write only service-level overrides such as port; their PID/process
// selectors live in catalog/services. Generic services have no catalog owner, so
// their PID question is prefilled from the init-script analysis: a pidfile path
// writes `pidfile:`, and if there is none, an exe derived from the unit offers a
// `command_match` selector.
func askServiceProps(p *Prompt, env Env, c DaemonCandidate, reviewPort bool) (string, map[string]any) {
	if _, exists := env.ServiceNames[c.Name]; exists {
		p.printf("  %q is already configured; skipping.\n", c.Name)
		return "", nil
	}
	body := map[string]any{"enabled": true}
	if c.Generic {
		unit := c.Unit
		if unit == "" {
			unit = c.Name
		}
		body["service"] = map[string]any{"name": unit}
		body["checks"] = map[string]any{"service": map[string]any{"type": "service", "expect": "active"}}
	} else {
		body["uses"] = c.Name
	}
	mergeServiceVariables(body, c.Variables)
	if reviewPort && c.Port > 0 {
		if n := p.AskInt(fmt.Sprintf("Port for %s?", c.Name), c.Port); n > 0 && n != c.Port {
			mergeServiceVariables(body, map[string]any{"port": n})
		}
	}
	if len(c.ConfigPaths) > 0 && p.Confirm(configCheckQuestion(c), true) {
		addConfigCheck(body, c.ConfigPaths)
	}
	if c.Generic {
		if pidfile := askServicePidfile(p, c); pidfile != "" {
			body["pidfile"] = pidfile
		} else if selector, label := detectedProcessSelector(c); selector != nil && p.Confirm("No pidfile — match "+c.Name+" by "+label+"?", true) {
			body["processes"] = map[string]any{"main": selector}
		}
	}
	return c.Name, body
}

func mergeServiceVariables(body map[string]any, vars map[string]any) {
	if len(vars) == 0 {
		return
	}
	dst, _ := body["variables"].(map[string]any)
	if dst == nil {
		dst = map[string]any{}
		body["variables"] = dst
	}
	for key, value := range vars {
		dst[key] = value
	}
}

func configCheckQuestion(c DaemonCandidate) string {
	label := "detected configuration file"
	if len(c.ConfigPaths) != 1 {
		label = fmt.Sprintf("%d detected configuration files", len(c.ConfigPaths))
	}
	return fmt.Sprintf("Add configuration check for %s (%s, every %s)?", c.Name, label, serviceConfigCheckInterval)
}

func addConfigCheck(body map[string]any, paths []string) {
	checks, _ := body["checks"].(map[string]any)
	if checks == nil {
		checks = map[string]any{}
		body["checks"] = checks
	}
	checks["config"] = map[string]any{
		"type":      "config",
		"path":      stringsToAny(paths),
		"on_change": true,
		"interval":  serviceConfigCheckInterval,
	}
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func askServicePidfile(p *Prompt, c DaemonCandidate) string {
	for {
		pidfile := strings.TrimSpace(p.Ask("Pidfile path for "+c.Name+" (blank to skip)", c.Pidfile))
		if pidfile == "" || filepath.IsAbs(pidfile) {
			return pidfile
		}
		p.printf("  pidfile must be an absolute path or blank\n")
	}
}

func detectedProcessSelector(c DaemonCandidate) (map[string]any, string) {
	selector := map[string]any{"type": "command_match"}
	if c.Cmd != "" {
		selector["cmd"] = c.Cmd
		if c.User != "" {
			selector["user"] = c.User
		}
		return selector, "command pattern " + c.Cmd
	}
	if c.Exe != "" {
		selector["exe"] = c.Exe
		if c.User != "" {
			selector["user"] = c.User
		}
		return selector, "executable " + c.Exe
	}
	return nil, ""
}

// serviceLabel renders the candidate's detected facts for the selection menu.
func serviceLabel(c DaemonCandidate) string {
	parts := []string{c.Title}
	if c.Unit != "" {
		parts = append(parts, "unit: "+c.Unit)
	}
	if c.Status != "" {
		parts = append(parts, "status: "+c.Status)
	}
	if c.Generic {
		parts = append(parts, "not in catalog")
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
	if c.Pidfile != "" {
		parts = append(parts, "pidfile: "+c.Pidfile)
	} else if c.Cmd != "" {
		parts = append(parts, "cmd match")
	} else if c.Exe != "" {
		parts = append(parts, "exe: "+c.Exe)
	}
	if !c.UnitPresent {
		parts = append(parts, "unit not found")
	}
	return strings.Join(parts, " · ")
}
