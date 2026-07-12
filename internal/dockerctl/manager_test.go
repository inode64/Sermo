package dockerctl

import (
	"context"
	"errors"
	"slices"
	"testing"

	"sermo/internal/servicemgr"
)

type fakeDockerClient struct {
	container Container
	actions   []string
}

func (f *fakeDockerClient) CloseIdleConnections() {}

func (f *fakeDockerClient) Inspect(context.Context, string) (Container, error) {
	return f.container, nil
}

func (f *fakeDockerClient) Start(context.Context, string) error {
	f.actions = append(f.actions, "start")
	return nil
}

func (f *fakeDockerClient) Stop(context.Context, string) error {
	f.actions = append(f.actions, "stop")
	return nil
}

func (f *fakeDockerClient) Unpause(context.Context, string) error {
	f.actions = append(f.actions, "unpause")
	return nil
}

func TestManagerStatusFromContainer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  ContainerState
		status servicemgr.Status
	}{
		{name: "running", state: ContainerState{Status: "running", Running: true}, status: servicemgr.StatusActive},
		{name: "paused", state: ContainerState{Status: "paused", Running: true, Paused: true}, status: servicemgr.StatusPaused},
		{name: "exited", state: ContainerState{Status: "exited"}, status: servicemgr.StatusInactive},
		{name: "restarting", state: ContainerState{Status: "restarting", Restarting: true}, status: servicemgr.StatusFailed},
		{name: "dead", state: ContainerState{Status: "dead", Dead: true}, status: servicemgr.StatusFailed},
		{name: "unknown", state: ContainerState{Status: "configured"}, status: servicemgr.StatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDockerClient{container: Container{State: tc.state}}
			manager := Manager{
				Spec: Spec{Container: "web"},
				NewClient: func(Spec) (DockerClient, error) {
					return fake, nil
				},
			}
			got, err := manager.Status(context.Background(), "svc")
			if err != nil {
				t.Fatalf("Status() error = %v", err)
			}
			if got.Status != tc.status || got.Backend != servicemgr.BackendDocker || got.Unit != "web" {
				t.Fatalf("Status() = %+v, want status %s docker/web", got, tc.status)
			}
		})
	}
}

func TestManagerActions(t *testing.T) {
	fake := &fakeDockerClient{}
	manager := Manager{
		Spec: Spec{Container: "web"},
		NewClient: func(Spec) (DockerClient, error) {
			return fake, nil
		},
	}
	if err := manager.Start(context.Background(), "svc"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := manager.Stop(context.Background(), "svc"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := manager.Resume(context.Background(), "svc"); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if !slices.Equal(fake.actions, []string{"start", "stop", "unpause"}) {
		t.Fatalf("actions = %v", fake.actions)
	}
	if err := manager.Restart(context.Background(), "svc"); err == nil {
		t.Fatal("Restart() must be composed by the operation engine")
	}
	if err := manager.Reload(context.Background(), "svc"); err == nil {
		t.Fatal("Reload() must be unsupported")
	}
}

func TestManagerPIDs(t *testing.T) {
	fake := &fakeDockerClient{container: Container{State: ContainerState{Pid: 4321}}}
	manager := Manager{
		Spec: Spec{Container: "web"},
		NewClient: func(Spec) (DockerClient, error) {
			return fake, nil
		},
	}
	pids, err := manager.PIDs(context.Background())
	if err != nil {
		t.Fatalf("PIDs() error = %v", err)
	}
	if !slices.Equal(pids, []int{4321}) {
		t.Fatalf("PIDs() = %v", pids)
	}
	if got := manager.BackendPIDs(context.Background())(); !slices.Equal(got, []int{4321}) {
		t.Fatalf("BackendPIDs() = %v", got)
	}
}

func TestManagerClientError(t *testing.T) {
	manager := Manager{
		Spec: Spec{Container: "web"},
		NewClient: func(Spec) (DockerClient, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := manager.Status(context.Background(), "svc"); err == nil {
		t.Fatal("Status() must return client error")
	}
}
