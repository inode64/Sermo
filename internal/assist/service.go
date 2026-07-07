package assist

import (
	"fmt"
	"path/filepath"
	"strings"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/process"
)

// serviceAssistant enables a catalog service as a monitored service. It detects
// the active init system (systemd/openrc) and each candidate's resolved unit,
// default port (and whether it is listening) and config locations, then writes a
// `kind: service` file that `uses:` the catalog service.
type serviceAssistant struct{}

const serviceConfigWatchInterval = "60m"
const serviceConfigWatchName = "config-files"
const serviceStatusWatchName = "service"

func (serviceAssistant) Name() string { return "service" }
func (serviceAssistant) Title() string {
	return "Monitor a system service (apache, nginx, mysql, …)"
}

func (serviceAssistant) Run(p *Prompt, env Env) (res Result, err error) {
	// Translate an input-closed re-prompt abort into ErrInputClosed even when
	// Run is driven directly (the CLI also recovers at its own boundary).
	defer Recover(&err)
	if env.CatalogServices == nil {
		return Result{}, fmt.Errorf("service detection is unavailable")
	}
	cands, err := env.CatalogServices()
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
	// detection from their catalog service profile, while generic services still need a
	// local PID source because they have no catalog owner. The shared
	// monitor-state + interval + dry-run mode come after, batched.
	type pending struct {
		name string
		body map[string]any
	}
	services := map[string]any{}
	addGroup := func(cands []ServiceCandidate, question string, allowNone bool) {
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
		if len(items) > 1 && p.Confirm("Apply the same monitor state, interval and dry-run mode to all selected services?", true) {
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

	return Result{
		Services: services,
		Summary:  resultSummary("service", services),
	}, nil
}

type serviceSettings struct {
	Monitoring
	DryRun bool
}

func askServiceSettings(p *Prompt, label string) serviceSettings {
	return serviceSettings{
		Monitoring: p.AskMonitoring(label),
		DryRun:     p.Confirm("Dry-run "+label+" automatic actions (evaluate but skip service actions and non-console notifications)?", false),
	}
}

func (s serviceSettings) apply(body map[string]any) {
	s.Monitoring.apply(body)
	if s.DryRun {
		body[config.EntryKeyDryRun] = true
	}
}

func splitServiceCandidates(cands []ServiceCandidate) (activeCatalog, generic []ServiceCandidate) {
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

func serviceCandidateActive(c ServiceCandidate) bool {
	return c.Status == "active"
}

func chooseServices(p *Prompt, question string, cands []ServiceCandidate, allowNone bool) []ServiceCandidate {
	labels := make([]string, len(cands))
	for i, c := range cands {
		labels[i] = serviceLabel(c)
	}
	var sel []int
	if allowNone {
		var keyword string
		sel, keyword = p.MultiChooseKeyword(question, labels, config.SelectionKeywordNone)
		if keyword == config.SelectionKeywordNone {
			return nil
		}
	} else {
		sel = p.MultiChoose(question, labels)
	}
	out := make([]ServiceCandidate, 0, len(sel))
	for _, idx := range sel {
		out = append(out, cands[idx])
	}
	return out
}

func groupHasPortDefaults(cands []ServiceCandidate) bool {
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
func askServiceProps(p *Prompt, env Env, c ServiceCandidate, reviewPort bool) (string, map[string]any) {
	if _, exists := env.ServiceNames[c.Name]; exists {
		p.printf("  %q is already configured; skipping.\n", c.Name)
		return "", nil
	}
	body := map[string]any{config.EntryKeyEnabled: true}
	if c.Generic {
		unit := c.Unit
		if unit == "" {
			unit = c.Name
		}
		body[config.ServiceKeyService] = unit
		addCheckOnlyWatch(body, serviceStatusWatchName, map[string]any{
			checks.CheckKeyType:   checks.CheckTypeService,
			checks.CheckKeyExpect: "active",
		})
	} else {
		body[config.ServiceKeyUses] = c.Name
	}
	mergeServiceVariables(body, c.Variables)
	if reviewPort && c.Port > 0 {
		if n := p.AskInt(fmt.Sprintf("Port for %s?", c.Name), c.Port); n > 0 && n != c.Port {
			mergeServiceVariables(body, map[string]any{config.VariableKeyPort: n})
		}
	}
	if len(c.ConfigPaths) > 0 && p.Confirm(configWatchQuestion(c), true) {
		addConfigWatch(body, c.ConfigPaths)
	}
	if c.Generic {
		if pidfile := askServicePidfile(p, c); pidfile != "" {
			body[config.ServiceKeyPidfile] = pidfile
		} else if selector, label := detectedProcessSelector(c); selector != nil && p.Confirm("No pidfile — match "+c.Name+" by "+label+"?", true) {
			body[config.SectionProcesses] = map[string]any{"main": selector}
		}
	}
	return c.Name, body
}

func mergeServiceVariables(body map[string]any, vars map[string]any) {
	if len(vars) == 0 {
		return
	}
	dst, _ := body[config.SectionVariables].(map[string]any)
	if dst == nil {
		dst = map[string]any{}
		body[config.SectionVariables] = dst
	}
	for key, value := range vars {
		dst[key] = value
	}
}

func configWatchQuestion(c ServiceCandidate) string {
	label := "detected configuration file"
	if len(c.ConfigPaths) != 1 {
		label = fmt.Sprintf("%d detected configuration files", len(c.ConfigPaths))
	}
	return fmt.Sprintf("Add configuration watch for %s (%s, every %s)?", c.Name, label, serviceConfigWatchInterval)
}

func addConfigWatch(body map[string]any, paths []string) {
	addCheckOnlyWatch(body, serviceConfigWatchName, map[string]any{
		checks.CheckKeyType:     checks.CheckTypeConfig,
		checks.CheckKeyPath:     stringsToAny(paths),
		checks.CheckKeyOnChange: true,
	}, map[string]any{config.EntryKeyInterval: serviceConfigWatchInterval})
}

func addCheckOnlyWatch(body map[string]any, name string, check map[string]any, fields ...map[string]any) {
	watches, _ := body[config.SectionWatches].(map[string]any)
	if watches == nil {
		watches = map[string]any{}
		body[config.SectionWatches] = watches
	}
	entry := map[string]any{config.WatchKeyCheck: check}
	for _, fieldSet := range fields {
		for key, value := range fieldSet {
			entry[key] = value
		}
	}
	watches[name] = entry
}

func stringsToAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func askServicePidfile(p *Prompt, c ServiceCandidate) string {
	for {
		pidfile := strings.TrimSpace(p.Ask("Pidfile path for "+c.Name+" (blank to skip)", c.Pidfile))
		if pidfile == "" || filepath.IsAbs(pidfile) {
			return pidfile
		}
		p.printf("  pidfile must be an absolute path or blank\n")
	}
}

func detectedProcessSelector(c ServiceCandidate) (map[string]any, string) {
	selector := map[string]any{}
	if c.Cmd != "" {
		selector[process.SelectorKeyCmd] = c.Cmd
		if c.User != "" {
			selector[process.SelectorKeyUser] = c.User
		}
		return selector, "command pattern " + c.Cmd
	}
	if c.Exe != "" {
		selector[process.SelectorKeyExe] = c.Exe
		if c.User != "" {
			selector[process.SelectorKeyUser] = c.User
		}
		return selector, "executable " + c.Exe
	}
	return nil, ""
}

// serviceLabel renders the candidate's detected facts for the selection menu.
func serviceLabel(c ServiceCandidate) string {
	details := []string{labelField("unit", c.Unit), labelField("status", c.Status)}
	if c.Generic {
		details = append(details, "not in catalog")
	}
	if c.Port > 0 {
		port := fmt.Sprintf("port %d", c.Port)
		if c.PortListening {
			port += " (listening)"
		}
		details = append(details, port)
	}
	if host, _ := c.Variables[config.VariableKeyHost].(string); host != "" {
		details = append(details, labelField(config.VariableKeyHost, host))
	}
	if len(c.ConfigPaths) > 0 {
		details = append(details, labelField("config", c.ConfigPaths[0]))
	}
	if c.Pidfile != "" {
		details = append(details, labelField(config.ServiceKeyPidfile, c.Pidfile))
	} else if c.Cmd != "" {
		details = append(details, "cmd match")
	} else if c.Exe != "" {
		details = append(details, labelField(process.SelectorKeyExe, c.Exe))
	}
	if !c.UnitPresent {
		details = append(details, "unit not found")
	}
	return detailLabel(c.Title, details...)
}
