package cli

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"

	"sermo/internal/assist"
	"sermo/internal/dockerctl"
	"sermo/internal/virt"
)

func listWizardDockerContainers(ctx context.Context, timeout time.Duration) ([]assist.DockerCandidate, error) {
	if _, err := os.Stat(dockerctl.DefaultSocket); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	client, err := dockerctl.NewClient(dockerctl.Spec{Socket: dockerctl.DefaultSocket})
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, wizardDetectionTimeout(timeout))
	defer cancel()
	containers, err := client.ListContainers(ctx, true)
	if err != nil {
		return nil, err
	}
	out := make([]assist.DockerCandidate, 0, len(containers))
	for _, container := range containers {
		name := dockerWizardContainerName(container)
		if name == "" {
			continue
		}
		out = append(out, assist.DockerCandidate{
			Name:      wizardManagedServiceName("docker", name),
			Title:     name,
			Container: name,
			Status:    container.State,
			Socket:    dockerctl.DefaultSocket,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func dockerWizardContainerName(container dockerctl.ContainerSummary) string {
	for _, name := range container.Names {
		name = strings.TrimPrefix(strings.TrimSpace(name), "/")
		if name != "" {
			return name
		}
	}
	id := strings.TrimSpace(container.ID)
	if len(id) > 12 {
		id = id[:12]
	}
	return id
}

func listWizardVMs(ctx context.Context, timeout time.Duration) ([]assist.VMCandidate, error) {
	if _, err := os.Stat(virt.DefaultSocket); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, wizardDetectionTimeout(timeout))
	defer cancel()
	domains, err := virt.ListDomains(ctx, virt.Spec{URI: virt.DefaultURI, Socket: virt.DefaultSocket})
	if err != nil {
		return nil, err
	}
	out := make([]assist.VMCandidate, 0, len(domains))
	for _, domain := range domains {
		name := strings.TrimSpace(domain.Name)
		if name == "" {
			continue
		}
		out = append(out, assist.VMCandidate{
			Name:   wizardManagedServiceName("vm", name),
			Title:  name,
			Domain: name,
			Status: string(domain.Status),
			URI:    virt.DefaultURI,
			Socket: virt.DefaultSocket,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func wizardManagedServiceName(prefix, target string) string {
	name := safeConfigPathName(prefix + "-" + strings.TrimPrefix(strings.TrimSpace(target), "/"))
	if name == "" {
		return prefix
	}
	return name
}

func wizardDetectionTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	return defaultTimeout("wizard")
}
