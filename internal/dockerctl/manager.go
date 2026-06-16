package dockerctl

import (
	"context"
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

// Start starts the configured container.
func (m Manager) Start(ctx context.Context, _ string) error {
	return m.withClient(func(c DockerClient) error {
		if err := c.Start(ctx, m.Spec.Container); err != nil {
			return fmt.Errorf("start container %q: %w", m.Spec.Container, err)
		}
		return nil
	})
}

// Stop asks Docker to stop the configured container without Docker-side SIGKILL
// escalation; see Client.Stop.
func (m Manager) Stop(ctx context.Context, _ string) error {
	return m.withClient(func(c DockerClient) error {
		if err := c.Stop(ctx, m.Spec.Container); err != nil {
			return fmt.Errorf("stop container %q: %w", m.Spec.Container, err)
		}
		return nil
	})
}

// Restart is composed by the safe operation engine as Stop+Start.
func (m Manager) Restart(context.Context, string) error {
	return fmt.Errorf("restart is composed by the operation engine")
}

// Reload is not meaningful for a Docker container.
func (m Manager) Reload(context.Context, string) error {
	return fmt.Errorf("reload is not supported for Docker containers")
}

// SupportsReload reports false for Docker containers.
func (m Manager) SupportsReload(context.Context, string) (bool, error) {
	return false, nil
}

// ResetState has no Docker equivalent; inspect reads the live container state.
func (m Manager) ResetState(context.Context, string) error {
	return nil
}

// Resume unpauses the configured container.
func (m Manager) Resume(ctx context.Context, _ string) error {
	return m.withClient(func(c DockerClient) error {
		if err := c.Unpause(ctx, m.Spec.Container); err != nil {
			return fmt.Errorf("resume container %q: %w", m.Spec.Container, err)
		}
		return nil
	})
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
func (m Manager) BackendPIDs() func() []int {
	return func() []int {
		ctx, cancel := context.WithTimeout(context.Background(), pidLookupTimeout)
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
	case state.Paused || status == "paused":
		return servicemgr.StatusPaused
	case state.Running && !state.Restarting && !state.Dead:
		return servicemgr.StatusActive
	case status == "created" || status == "exited":
		return servicemgr.StatusInactive
	case state.Restarting || state.Dead || status == "restarting" || status == "dead" || status == "removing":
		return servicemgr.StatusFailed
	default:
		return servicemgr.StatusUnknown
	}
}
