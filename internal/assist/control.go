package assist

import (
	"errors"
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

//nolint:dupl // spec-driven Run: the body is already one runControlledAssistant call whose fields are this assistant's operator-facing strings and detectors.
func (dockerAssistant) Run(p *Prompt, env Env) (res Result, err error) {
	defer Recover(&err)
	return runControlledAssistant(p, env, controlledAssistantSpec[DockerCandidate]{
		detect:      env.DockerContainers,
		unavailable: "docker detection is unavailable",
		detectLabel: "detect Docker containers",
		noneFound:   "no Docker containers were detected on this host",
		question:    "Which Docker containers do you want Sermo to monitor and manage?",
		choose:      chooseDockerContainers,
		name:        dockerName,
		build:       buildDockerService,
	})
}

type vmAssistant struct{}

func (vmAssistant) Name() string { return AssistantNameVM }
func (vmAssistant) Title() string {
	return "Monitor and manage libvirt/QEMU virtual machines"
}

//nolint:dupl // spec-driven Run; see dockerAssistant.Run.
func (vmAssistant) Run(p *Prompt, env Env) (res Result, err error) {
	defer Recover(&err)
	return runControlledAssistant(p, env, controlledAssistantSpec[VMCandidate]{
		detect:      env.VMs,
		unavailable: "VM detection is unavailable",
		detectLabel: "detect libvirt domains",
		noneFound:   "no libvirt/QEMU domains were detected on this host",
		question:    "Which virtual machines do you want Sermo to monitor and manage?",
		choose:      chooseVMs,
		name:        vmName,
		build:       buildVMService,
	})
}

type controlledAssistantSpec[T any] struct {
	detect      func() ([]T, error)
	unavailable string
	detectLabel string
	noneFound   string
	question    string
	choose      func(*Prompt, string, []T) []T
	name        func(T) string
	build       func(T) map[string]any
}

func runControlledAssistant[T any](p *Prompt, env Env, spec controlledAssistantSpec[T]) (Result, error) {
	if spec.detect == nil {
		return Result{}, errors.New(spec.unavailable)
	}
	candidates, err := spec.detect()
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", spec.detectLabel, err)
	}
	if len(candidates) == 0 {
		return Result{}, errors.New(spec.noneFound)
	}
	selected := spec.choose(p, spec.question, candidates)
	return controlledResult(buildControlledServices(p, env, selected, spec.name, spec.build)), nil
}

func buildControlledServices[T any](p *Prompt, env Env, selected []T, name func(T) string, build func(T) map[string]any) map[string]any {
	services := map[string]any{}
	selected = pendingControlledCandidates(p, env, selected, name)
	applyServiceSettings(p, selected, name, func(candidate T, settings serviceSettings) {
		body := build(candidate)
		settings.apply(body)
		services[name(candidate)] = body
	})
	return services
}

func pendingControlledCandidates[T any](p *Prompt, env Env, selected []T, name func(T) string) []T {
	seen := map[string]struct{}{}
	pending := make([]T, 0, len(selected))
	for _, candidate := range selected {
		candidateName := name(candidate)
		if _, exists := env.ServiceNames[candidateName]; exists {
			p.printf("  %q is already configured; skipping.\n", candidateName)
			continue
		}
		if _, exists := seen[candidateName]; exists {
			p.printf("  %q was already selected; skipping duplicate.\n", candidateName)
			continue
		}
		seen[candidateName] = struct{}{}
		pending = append(pending, candidate)
	}
	return pending
}

func controlledResult(services map[string]any) Result {
	if len(services) == 0 {
		return Result{}
	}
	return Result{
		Services: services,
		Summary:  resultSummary(AssistantNameService, services),
	}
}

func chooseDockerContainers(p *Prompt, question string, cands []DockerCandidate) []DockerCandidate {
	return chooseCandidates(p, question, cands, dockerLabel)
}

func chooseVMs(p *Prompt, question string, cands []VMCandidate) []VMCandidate {
	return chooseCandidates(p, question, cands, vmLabel)
}

// runningExpect is the `expect:` mapping both controlled-service checks use to
// require a running target: one probe field compared for equality.
func runningExpect(field, want string) map[string]any {
	return map[string]any{
		field: map[string]any{checks.CheckKeyOp: cfgval.CompareOpEqual, checks.CheckKeyValue: want},
	}
}

// attachSocket records an explicit control socket on both the control block and
// the check that watches it, when the candidate reported one.
func attachSocket(control, check map[string]any, controlKey, socket string) {
	if socket == "" {
		return
	}
	control[controlKey] = socket
	check[checks.CheckKeySocket] = socket
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
		checks.CheckKeyExpect:    runningExpect(conn.ExtraKeyContainerStatus, conn.DockerContainerStatusRunning),
	}
	attachSocket(control, check, dockerctl.ControlKeySocket, c.Socket)
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
		checks.CheckKeyExpect:   runningExpect(conn.ExtraKeyDomainState, conn.LibvirtDomainStateRunning),
	}
	if c.URI != "" {
		control[virt.ControlKeyURI] = c.URI
		check[checks.CheckKeyQuery] = c.URI
	}
	attachSocket(control, check, virt.ControlKeySocket, c.Socket)
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
