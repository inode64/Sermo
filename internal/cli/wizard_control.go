package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"sermo/internal/assist"
	"sermo/internal/dockerctl"
	"sermo/internal/execx"
	"sermo/internal/hostfs"
	"sermo/internal/virt"
)

const dockerContainerIDShortLength = 12

func (a App) runDockerDiscovery(ctx context.Context, opts options) int {
	var candidates []assist.DockerCandidate
	var err error
	if a.wizardEnvFunc != nil {
		candidates, err = a.wizardEnvFunc(nil).DockerContainers()
	} else {
		candidates, err = listWizardDockerContainers(ctx, opts.timeout, a.Runner)
	}
	if err != nil {
		a.reportError(opts, "Docker discovery: "+err.Error())
		return exitRuntimeError
	}
	if err := json.NewEncoder(a.Stdout).Encode(candidates); err != nil {
		a.reportError(opts, "encode Docker discovery: "+err.Error())
		return exitRuntimeError
	}
	return exitSuccess
}

func listWizardDockerContainers(ctx context.Context, timeout time.Duration, runner execx.Runner) ([]assist.DockerCandidate, error) {
	if _, err := hostfs.Stat(dockerctl.DefaultSocket); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat docker socket %s: %w", dockerctl.DefaultSocket, err)
	}
	client := dockerctl.NewClient(dockerctl.Spec{Socket: dockerctl.DefaultSocket})
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	containers, err := client.ListContainers(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("list docker containers: %w", err)
	}
	out := make([]assist.DockerCandidate, 0, len(containers))
	for _, container := range containers {
		name := dockerWizardContainerName(container)
		if name == "" {
			continue
		}
		candidate := assist.DockerCandidate{
			Name:      wizardManagedServiceName(dockerctl.ControlType, name),
			Title:     name,
			Container: name,
			Status:    container.State,
			Socket:    dockerctl.DefaultSocket,
		}
		candidate.Compose, candidate.RecoveryWarning = wizardDockerCompose(ctx, container, name, runner)
		out = append(out, candidate)
	}
	slices.SortFunc(out, func(a, b assist.DockerCandidate) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

func wizardDockerCompose(ctx context.Context, c dockerctl.ContainerSummary, name string, runner execx.Runner) (map[string]any, string) {
	spec, present, err := dockerctl.ComposeFromLabels(c.Labels, c.ImageID)
	if err != nil {
		return nil, err.Error()
	}
	if !present {
		return nil, "no Compose source metadata"
	}
	manager := dockerctl.NewManager(dockerctl.Spec{Container: name, Socket: dockerctl.DefaultSocket, Compose: &spec})
	manager.Runner = runner
	if err := manager.ValidateCompose(ctx); err != nil {
		return nil, err.Error()
	}
	return spec.Tree(), ""
}

func dockerWizardContainerName(container dockerctl.ContainerSummary) string {
	for _, name := range container.Names {
		name = strings.TrimPrefix(strings.TrimSpace(name), "/")
		if name != "" {
			return name
		}
	}
	id := strings.TrimSpace(container.ID)
	if len(id) > dockerContainerIDShortLength {
		id = id[:dockerContainerIDShortLength]
	}
	return id
}

func listWizardVMs(ctx context.Context, timeout time.Duration) ([]assist.VMCandidate, error) {
	socket, ok, err := virt.FirstExistingLocalSocket(localSocketExists)
	if err != nil {
		return nil, fmt.Errorf("find libvirt socket: %w", err)
	}
	if !ok {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	domains, err := virt.ListDomains(ctx, virt.Spec{URI: virt.DefaultURI, Socket: socket})
	if err != nil {
		return nil, fmt.Errorf("list libvirt domains: %w", err)
	}
	out := make([]assist.VMCandidate, 0, len(domains))
	for _, domain := range domains {
		name := strings.TrimSpace(domain.Name)
		if name == "" {
			continue
		}
		out = append(out, assist.VMCandidate{
			Name:   wizardManagedServiceName(serviceFamilyVM, name),
			Title:  name,
			Domain: name,
			Status: string(domain.Status),
			URI:    virt.DefaultURI,
			Socket: socket,
		})
	}
	slices.SortFunc(out, func(a, b assist.VMCandidate) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

func localSocketExists(path string) (bool, error) {
	if _, err := hostfs.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return true, nil
}

func wizardManagedServiceName(prefix, target string) string {
	name := safeConfigPathName(prefix + "-" + strings.TrimPrefix(strings.TrimSpace(target), "/"))
	if name == "" {
		return prefix
	}
	return name
}
