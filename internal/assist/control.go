package assist

import "fmt"

type dockerAssistant struct{}

func (dockerAssistant) Name() string { return "docker" }
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

func (vmAssistant) Name() string { return "vm" }
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
		Summary:  resultSummary("service", services),
	}, nil
}

func chooseDockerContainers(p *Prompt, question string, cands []DockerCandidate) []DockerCandidate {
	return chooseCandidates(p, question, cands, dockerLabel)
}

func chooseVMs(p *Prompt, question string, cands []VMCandidate) []VMCandidate {
	return chooseCandidates(p, question, cands, vmLabel)
}

func chooseCandidates[T any](p *Prompt, question string, cands []T, label func(T) string) []T {
	labels := make([]string, len(cands))
	for i, c := range cands {
		labels[i] = label(c)
	}
	sel := p.MultiChoose(question, labels)
	out := make([]T, 0, len(sel))
	for _, idx := range sel {
		out = append(out, cands[idx])
	}
	return out
}

func candidateNames[T any](cands []T, name func(T) string) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = name(c)
	}
	return out
}

func buildDockerService(c DockerCandidate) map[string]any {
	control := map[string]any{"type": "docker", "container": c.Container}
	check := map[string]any{
		"type":      "docker",
		"container": c.Container,
		"on_change": true,
		"expect": map[string]any{
			"container.status": map[string]any{"op": "==", "value": "running"},
		},
	}
	if c.Socket != "" {
		control["socket"] = c.Socket
		check["socket"] = c.Socket
	}
	return controlledService(control, "docker", check)
}

func buildVMService(c VMCandidate) map[string]any {
	control := map[string]any{"type": "libvirt", "domain": c.Domain}
	check := map[string]any{
		"type":      "libvirt",
		"domain":    c.Domain,
		"on_change": true,
		"expect": map[string]any{
			"domain.state": map[string]any{"op": "==", "value": "running"},
		},
	}
	if c.URI != "" {
		control["uri"] = c.URI
		check["query"] = c.URI
	}
	if c.Socket != "" {
		control["socket"] = c.Socket
		check["socket"] = c.Socket
	}
	return controlledService(control, "vm", check)
}

func dockerName(c DockerCandidate) string {
	return c.Name
}

func vmName(c VMCandidate) string {
	return c.Name
}

func controlledService(control map[string]any, checkName string, check map[string]any) map[string]any {
	return map[string]any{
		"enabled": true,
		"control": control,
		"watches": map[string]any{
			checkName: map[string]any{"check": check},
		},
	}
}

func dockerLabel(c DockerCandidate) string {
	return detailLabel(c.Title, labelField("container", c.Container), labelField("status", c.Status))
}

func vmLabel(c VMCandidate) string {
	return detailLabel(c.Title, labelField("domain", c.Domain), labelField("status", c.Status))
}
