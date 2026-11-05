package dockerctl

import (
	"context"
	"errors"
	"slices"
	"testing"

	"sermo/internal/servicemgr"
)

const webContainerID = "4f1c0de5a9b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c"

type fakeDockerClient struct {
	container Container
	actions   []string
	targets   []string
}

func (f *fakeDockerClient) CloseIdleConnections() {}

// Inspect answers like the engine for a configured "web": the container named
// web, unless a test sets another one (a unique ID prefix match).
func (f *fakeDockerClient) Inspect(context.Context, string) (Container, error) {
	c := f.container
	if c.Name == "" && c.ID == "" {
		c.Name, c.ID = "/web", webContainerID
	}
	return c, nil
}

func (f *fakeDockerClient) act(verb, target string) error {
	f.actions = append(f.actions, verb)
	f.targets = append(f.targets, target)
	return nil
}

func (f *fakeDockerClient) Start(_ context.Context, target string) error {
	return f.act("start", target)
}

func (f *fakeDockerClient) Stop(_ context.Context, target string) error {
	return f.act("stop", target)
}

func (f *fakeDockerClient) Unpause(_ context.Context, target string) error {
	return f.act("unpause", target)
}

func (f *fakeDockerClient) Pause(_ context.Context, target string) error {
	return f.act("pause", target)
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
	if err := manager.Pause(context.Background(), "svc"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if err := manager.Resume(context.Background(), "svc"); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if !slices.Equal(fake.actions, []string{"start", "stop", "pause", "unpause"}) {
		t.Fatalf("actions = %v", fake.actions)
	}
	for _, target := range fake.targets {
		if target != webContainerID {
			t.Fatalf("action targeted %q, want the inspected full ID", target)
		}
	}
	if err := manager.Reload(context.Background(), "svc"); err == nil {
		t.Fatal("Reload() must be unsupported")
	}
}

// Docker resolves a reference by exact name, full ID or unique ID prefix. A
// configured value that is no live name may be another container's ID prefix:
// nothing may act on, or seed discovery from, a container that is neither the
// configured name nor the configured full ID.
func TestManagerRefusesContainerResolvedByAnotherReference(t *testing.T) {
	other := Container{ID: "4f1c" + webContainerID[4:], Name: "/db", State: ContainerState{Status: "running", Running: true, Pid: 99}}
	fake := &fakeDockerClient{container: other}
	manager := Manager{
		Spec:      Spec{Container: "4f1c"},
		NewClient: func(Spec) (DockerClient, error) { return fake, nil },
	}
	for name, act := range map[string]func(context.Context, string) error{
		"start": manager.Start, "stop": manager.Stop, "pause": manager.Pause, "resume": manager.Resume,
	} {
		if err := act(context.Background(), "svc"); err == nil {
			t.Fatalf("%s acted on %s through the prefix %q", name, other.ContainerName(), manager.Spec.Container)
		}
	}
	if len(fake.actions) != 0 {
		t.Fatalf("actions = %v, want none", fake.actions)
	}
	if _, err := manager.Status(context.Background(), "svc"); err == nil {
		t.Fatal("Status reported another container")
	}
	if pids, err := manager.PIDs(context.Background()); err == nil || len(pids) != 0 {
		t.Fatalf("PIDs = %v, %v; want no seed from another container", pids, err)
	}

	byID := Manager{
		Spec:      Spec{Container: webContainerID},
		NewClient: func(Spec) (DockerClient, error) { return &fakeDockerClient{}, nil },
	}
	if err := byID.Stop(context.Background(), "svc"); err != nil {
		t.Fatalf("a configured full ID must still resolve: %v", err)
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
