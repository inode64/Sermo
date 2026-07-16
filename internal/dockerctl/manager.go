package dockerctl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"sermo/internal/servicemgr"
)

const pidLookupTimeout = 5 * time.Second

// DockerClient is the Docker surface Manager needs. Tests inject a fake.
type DockerClient interface {
	CloseIdleConnections()
	Inspect(ctx context.Context, container string) (Container, error)
	Start(ctx context.Context, container string) error
	Stop(ctx context.Context, container string) error
	Unpause(ctx context.Context, container string) error
}

// Manager implements service management over one Docker container.
type Manager struct {
	servicemgr.ComposedRestart

	Spec      Spec
	NewClient func(Spec) (DockerClient, error)
}

var _ servicemgr.Manager = Manager{}

// NewManager returns a Docker manager for spec.
func NewManager(spec Spec) Manager {
	return Manager{Spec: spec}
}

// Status returns the normalized state of the managed container.
func (m Manager) Status(ctx context.Context, service string) (servicemgr.ServiceStatus, error) {
	container, err := m.inspect(ctx)
	if err != nil {
		return servicemgr.ServiceStatus{}, err
	}
	return servicemgr.ServiceStatus{
		Service: service,
		Backend: servicemgr.BackendDocker,
		Unit:    m.Spec.Container,
		Status:  statusFromContainer(container),
	}, nil
}

// withContainerAction runs act against the configured container and wraps any
// error with the action verb; the Docker counterpart of virt's withDomainAction.
func (m Manager) withContainerAction(ctx context.Context, verb string, act func(DockerClient, context.Context, string) error) error {
	return m.withClient(func(c DockerClient) error {
		if err := act(c, ctx, m.Spec.Container); err != nil {
			return fmt.Errorf("%s container %q: %w", verb, m.Spec.Container, err)
		}
		return nil
	})
}

// Start starts the configured container.
func (m Manager) Start(ctx context.Context, _ string) error {
	return m.withContainerAction(ctx, "start", DockerClient.Start)
}

// Stop asks Docker to stop the configured container without Docker-side SIGKILL
// escalation; see Client.Stop.
func (m Manager) Stop(ctx context.Context, _ string) error {
	return m.withContainerAction(ctx, "stop", DockerClient.Stop)
}

// Reload is not meaningful for a Docker container. Restart, SupportsReload and
// ResetState come from the embedded servicemgr.ComposedRestart.
func (m Manager) Reload(context.Context, string) error {
	return errors.New("reload is not supported for Docker containers")
}

// Resume unpauses the configured container.
func (m Manager) Resume(ctx context.Context, _ string) error {
	return m.withContainerAction(ctx, "resume", DockerClient.Unpause)
}

// PIDs returns the container's init PID as a process-discovery seed.
func (m Manager) PIDs(ctx context.Context) ([]int, error) {
	container, err := m.inspect(ctx)
	if err != nil {
		return nil, err
	}
	if container.State.Pid <= 0 {
		return nil, nil
	}
	return []int{container.State.Pid}, nil
}

// BackendPIDs returns a bounded process-discovery source for Sermo's discoverer.
func (m Manager) BackendPIDs(ctx context.Context) func() []int {
	return func() []int {
		ctx, cancel := context.WithTimeout(ctx, pidLookupTimeout)
		defer cancel()
		pids, err := m.PIDs(ctx)
		if err != nil {
			return nil
		}
		return pids
	}
}

func (m Manager) inspect(ctx context.Context) (Container, error) {
	var out Container
	err := m.withClient(func(c DockerClient) error {
		container, err := c.Inspect(ctx, m.Spec.Container)
		if err != nil {
			return fmt.Errorf("container %q: %w", m.Spec.Container, err)
		}
		out = container
		return nil
	})
	return out, err
}

func (m Manager) withClient(fn func(DockerClient) error) error {
	client, err := m.client()
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	return fn(client)
}

func (m Manager) client() (DockerClient, error) {
	if m.NewClient != nil {
		return m.NewClient(m.Spec)
	}
	return NewClient(m.Spec)
}

func statusFromContainer(c Container) servicemgr.Status {
	state := c.State
	status := strings.ToLower(strings.TrimSpace(state.Status))
	switch {
	case state.Paused || status == ContainerStatusPaused:
		return servicemgr.StatusPaused
	case state.Running && !state.Restarting && !state.Dead:
		return servicemgr.StatusActive
	case status == ContainerStatusCreated || status == ContainerStatusExited:
		return servicemgr.StatusInactive
	case state.Restarting || state.Dead || status == ContainerStatusRestarting || status == ContainerStatusDead || status == ContainerStatusRemoving:
		return servicemgr.StatusFailed
	default:
		return servicemgr.StatusUnknown
	}
}
