package assist

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strings"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// serviceAssistant enables a catalog service as a monitored service. It detects
// the active init system (systemd/openrc) and each candidate's resolved unit,
// default port (and whether it is listening) and config locations, then writes a
// `kind: service` file that `uses:` the catalog service.
type serviceAssistant struct{}

const serviceConfigWatchInterval = "60m"
const serviceConfigWatchName = "config-files"
const serviceStatusWatchName = AssistantNameService

type pendingService struct {
	name string
	body map[string]any
}

func (serviceAssistant) Name() string { return AssistantNameService }
func (serviceAssistant) Title() string {
	return "Monitor a system service (apache, nginx, mysql, …)"
}

func (serviceAssistant) Run(p *Prompt, env Env) (res Result, err error) {
	// Translate an input-closed re-prompt abort into ErrInputClosed even when
	// Run is driven directly (the CLI also recovers at its own boundary).
	defer Recover(&err)
	if env.CatalogServices == nil {
		return Result{}, errors.New("service detection is unavailable")
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
		return Result{}, errors.New("no active services were detected on this host")
	}

	services := map[string]any{}

	if len(activeCatalog) > 0 {
		addServiceGroup(p, env, services, activeCatalog, "Which active catalog services do you want to monitor?", false)
	} else {
		p.printf("No active catalog services were detected.\n\n")
	}
	if len(generic) > 0 && p.Confirm("Review active services without catalog profiles?", false) {
		addServiceGroup(p, env, services, generic, "Which uncataloged active services do you want to monitor?", true)
	}
	return controlledResult(services), nil
}

// addServiceGroup gathers the per-service properties before asking about shared
// monitoring settings, so services already present in the configuration are
// skipped without consuming further wizard answers.
func addServiceGroup(p *Prompt, env Env, services map[string]any, cands []ServiceCandidate, question string, allowNone bool) {
	selected := chooseServices(p, question, cands, allowNone)
	items := pendingServiceItems(p, env, selected)
	applyPendingServiceSettings(p, items, services)
}

func pendingServiceItems(p *Prompt, env Env, selected []ServiceCandidate) []pendingService {
	if len(selected) == 0 {
		return nil
	}
	reviewPorts := len(selected) == 1
	if len(selected) > 1 && groupHasPortDefaults(selected) {
		reviewPorts = p.Confirm("Review per-service port overrides?", false)
	}
	items := make([]pendingService, 0, len(selected))
	for _, candidate := range selected {
		name, body := askServiceProps(p, env, candidate, reviewPorts)
		if name != "" {
			items = append(items, pendingService{name, body})
		}
	}
	return items
}

func applyPendingServiceSettings(p *Prompt, items []pendingService, services map[string]any) {
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = item.name
	}
	applyControlledSettings(p, names, func(name string, settings serviceSettings) {
		for _, item := range items {
			if item.name != name {
				continue
			}
			settings.apply(item.body)
			services[name] = item.body
			return
		}
	})
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
	return c.Status == string(servicemgr.StatusActive)
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
			checks.CheckKeyExpect: string(servicemgr.StatusActive),
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
			body[config.SectionProcesses] = map[string]any{process.RoleMain: selector}
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
	maps.Copy(dst, vars)
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
		maps.Copy(entry, fieldSet)
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
	details := []string{labelField(labelFieldUnit, c.Unit), labelField(labelFieldStatus, c.Status)}
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
		details = append(details, labelField(labelFieldConfig, c.ConfigPaths[0]))
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
