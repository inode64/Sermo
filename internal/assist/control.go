package assist

import (
	"fmt"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/conn"
	"sermo/internal/dockerctl"
	"sermo/internal/virt"
)

type dockerAssistant struct{}

func (dockerAssistant) Name() string { return dockerctl.ControlType }
func (dockerAssistant) Title() string {
	return "Monitor and manage Docker containers"
}

func (dockerAssistant) Run(p *Prompt, env Env) (res Result, err error) {
	defer Recover(&err)
	if env.DockerContainers == nil {
		return Result{}, fmt.Errorf("docker detection is unavailable")
	}
	cands, err := env.DockerContainers()
	if err != nil {
		return Result{}, fmt.Errorf("detect Docker containers: %w", err)
	}
	if len(cands) == 0 {
		return Result{}, fmt.Errorf("no Docker containers were detected on this host")
	}
	selected := chooseDockerContainers(p, "Which Docker containers do you want Sermo to monitor and manage?", cands)
	return controlledResult(buildControlledServices(p, env, selected, dockerName, buildDockerService))
}

type vmAssistant struct{}

func (vmAssistant) Name() string { return AssistantNameVM }
func (vmAssistant) Title() string {
	return "Monitor and manage libvirt/QEMU virtual machines"
}

func (vmAssistant) Run(p *Prompt, env Env) (res Result, err error) {
	defer Recover(&err)
	if env.VMs == nil {
		return Result{}, fmt.Errorf("VM detection is unavailable")
	}
	cands, err := env.VMs()
	if err != nil {
		return Result{}, fmt.Errorf("detect libvirt domains: %w", err)
	}
	if len(cands) == 0 {
		return Result{}, fmt.Errorf("no libvirt/QEMU domains were detected on this host")
	}
	selected := chooseVMs(p, "Which virtual machines do you want Sermo to monitor and manage?", cands)
	return controlledResult(buildControlledServices(p, env, selected, vmName, buildVMService))
}

func buildControlledServices[T any](p *Prompt, env Env, selected []T, name func(T) string, build func(T) map[string]any) map[string]any {
	services := map[string]any{}
	names := candidateNames(selected, name)
	applyControlledSettings(p, names, func(target string, settings serviceSettings) {
		for _, candidate := range selected {
			candidateName := name(candidate)
			if candidateName != target {
				continue
			}
			if addControlledService(p, env, services, candidateName, build(candidate), settings) {
				break
			}
		}
	})
	return services
}

func applyControlledSettings(p *Prompt, names []string, apply func(string, serviceSettings)) {
	if len(names) == 0 {
		return
	}
	var shared *serviceSettings
	if len(names) > 1 && p.Confirm("Apply the same monitor state, interval and dry-run mode to all selected services?", true) {
		s := askServiceSettings(p, "all selected services")
		shared = &s
	}
	for _, name := range names {
		settings := shared
		if settings == nil {
			s := askServiceSettings(p, name)
			settings = &s
		}
		apply(name, *settings)
	}
}

func addControlledService(p *Prompt, env Env, services map[string]any, name string, body map[string]any, settings serviceSettings) bool {
	if _, exists := env.ServiceNames[name]; exists {
		p.printf("  %q is already configured; skipping.\n", name)
		return false
	}
	if _, exists := services[name]; exists {
		p.printf("  %q was already selected; skipping duplicate.\n", name)
		return false
	}
	settings.apply(body)
	services[name] = body
	return true
}

func controlledResult(services map[string]any) (Result, error) {
	if len(services) == 0 {
		return Result{}, nil
	}
	return Result{
		Services: services,
		Summary:  resultSummary(AssistantNameService, services),
	}, nil
}

func chooseDockerContainers(p *Prompt, question string, cands []DockerCandidate) []DockerCandidate {
	return chooseCandidates(p, question, cands, dockerLabel)
}

func chooseVMs(p *Prompt, question string, cands []VMCandidate) []VMCandidate {
	return chooseCandidates(p, question, cands, vmLabel)
}

func buildDockerService(c DockerCandidate) map[string]any {
	control := map[string]any{
		dockerctl.ControlKeyType:      dockerctl.ControlType,
		dockerctl.ControlKeyContainer: c.Container,
	}
	check := map[string]any{
		checks.CheckKeyType:      dockerctl.ControlType,
		checks.CheckKeyContainer: c.Container,
		checks.CheckKeyOnChange:  true,
		checks.CheckKeyExpect: map[string]any{
			conn.ExtraKeyContainerStatus: map[string]any{checks.CheckKeyOp: cfgval.CompareOpEqual, checks.CheckKeyValue: conn.DockerContainerStatusRunning},
		},
	}
	if c.Socket != "" {
		control[dockerctl.ControlKeySocket] = c.Socket
		check[checks.CheckKeySocket] = c.Socket
	}
	return controlledService(control, dockerctl.ControlType, check)
}

func buildVMService(c VMCandidate) map[string]any {
	control := map[string]any{
		virt.ControlKeyType:   virt.ControlType,
		virt.ControlKeyDomain: c.Domain,
	}
	check := map[string]any{
		checks.CheckKeyType:     virt.ControlType,
		checks.CheckKeyDomain:   c.Domain,
		checks.CheckKeyOnChange: true,
		checks.CheckKeyExpect: map[string]any{
			conn.ExtraKeyDomainState: map[string]any{checks.CheckKeyOp: cfgval.CompareOpEqual, checks.CheckKeyValue: conn.LibvirtDomainStateRunning},
		},
	}
	if c.URI != "" {
		control[virt.ControlKeyURI] = c.URI
		check[checks.CheckKeyQuery] = c.URI
	}
	if c.Socket != "" {
		control[virt.ControlKeySocket] = c.Socket
		check[checks.CheckKeySocket] = c.Socket
	}
	return controlledService(control, AssistantNameVM, check)
}

func dockerName(c DockerCandidate) string {
	return c.Name
}

func vmName(c VMCandidate) string {
	return c.Name
}

func controlledService(control map[string]any, checkName string, check map[string]any) map[string]any {
	return map[string]any{
		config.EntryKeyEnabled: true,
		config.SectionControl:  control,
		config.SectionWatches: map[string]any{
			checkName: map[string]any{config.WatchKeyCheck: check},
		},
	}
}

func dockerLabel(c DockerCandidate) string {
	return detailLabel(c.Title, labelField(labelFieldContainer, c.Container), labelField(labelFieldStatus, c.Status))
}

func vmLabel(c VMCandidate) string {
	return detailLabel(c.Title, labelField(labelFieldDomain, c.Domain), labelField(labelFieldStatus, c.Status))
}
