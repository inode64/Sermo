package dockerctl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
	"sermo/internal/servicemgr"
)

type recoveryClient struct {
	fakeDockerClient
	runner       *execxtest.Runner
	image        Image
	imageErr     error
	resourcesErr error
	containers   []ContainerSummary
	listErr      error
	listCalls    int
	inspectErr   error
	concurrent   bool
	wrongImage   bool
	started      string
}

func (c *recoveryClient) Inspect(_ context.Context, _ string) (Container, error) {
	if c.inspectErr != nil {
		return Container{}, c.inspectErr
	}
	if c.container.ID != "" {
		return c.container, nil
	}
	for _, call := range c.runner.Calls() {
		if call.Input == "" {
			continue
		}
		var document struct {
			Services map[string]struct {
				Labels map[string]string `json:"labels"`
			} `json:"services"`
		}
		if err := json.Unmarshal([]byte(call.Input), &document); err != nil {
			return Container{}, err
		}
		labels := document.Services["app"].Labels
		labels[composeProjectLabel], labels[composeServiceLabel] = "project", "app"
		if c.concurrent {
			labels[composeRecoveryLabel] = "foreign-operation"
		}
		image := c.image.ID
		if c.wrongImage {
			image = "different-image"
		}
		state := ContainerState{Status: ContainerStatusCreated}
		if c.started != "" {
			state = ContainerState{Status: ContainerStatusRunning, Running: true, Pid: 42}
		}
		return Container{ID: "created-id", Name: "/web", Image: image, Config: ContainerConfig{Labels: labels}, State: state}, nil
	}
	return Container{}, servicemgr.ErrNotFound
}

func (c *recoveryClient) ListContainers(context.Context, bool) ([]ContainerSummary, error) {
	c.listCalls++
	return c.containers, c.listErr
}

func (c *recoveryClient) Image(context.Context, string) (Image, error) {
	return c.image, c.imageErr
}

func (c *recoveryClient) Resource(context.Context, string, string) error {
	return c.resourcesErr
}

func (c *recoveryClient) Start(_ context.Context, id string) error {
	c.started = id
	return nil
}

func recoveryFixture(t *testing.T) (Manager, *recoveryClient, map[string]any) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yml")
	if err := os.WriteFile(file, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	image := Image{ID: "sha256:" + strings.Repeat("a", 64)}
	image.Config.Volumes = map[string]any{"/data": map[string]any{}}
	client := &recoveryClient{runner: &execxtest.Runner{RespectContext: true}, image: image}
	manager := Manager{
		Spec:      Spec{Socket: DefaultSocket, Container: "web", Compose: &ComposeSpec{Project: "project", Service: "app", Directory: dir, Files: []string{file}, Image: image.ID}},
		Runner:    client.runner,
		NewClient: func(Spec) (DockerClient, error) { return client, nil },
	}
	model := map[string]any{
		"name": "project",
		"services": map[string]any{
			"app": map[string]any{
				"image": "example/app:latest", "container_name": "web",
				"environment": map[string]any{"PASSWORD": "$literal${keep}"},
				"depends_on":  map[string]any{"db": map[string]any{"condition": "service_started"}},
				"volumes":     []any{map[string]any{"type": "volume", "source": "data", "target": "/data"}},
				"networks":    map[string]any{"default": nil},
			},
			"db": map[string]any{"image": "example/db"},
		},
		"volumes":  map[string]any{"data": map[string]any{"name": "project_data"}},
		"networks": map[string]any{"default": map[string]any{"name": "project_default"}},
	}
	setComposeOutput(t, client.runner, model)
	return manager, client, model
}

func setComposeOutput(t *testing.T, runner *execxtest.Runner, model map[string]any) {
	t.Helper()
	body, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	runner.Queue = []execx.Result{{Stdout: strings.ReplaceAll(string(body), "$", "$$")}, {}}
}

func TestComposeRecreationPinsAndIsolates(t *testing.T) {
	m, client, _ := recoveryFixture(t)
	t.Setenv("DOCKER_CONTEXT", "wrong-daemon")
	t.Setenv("COMPOSE_REMOVE_ORPHANS", "1")
	id, err := m.Create(t.Context())
	if err != nil || id != "created-id" || client.started != "" {
		t.Fatalf("Create = %q, %v, started=%q", id, err, client.started)
	}
	calls := client.runner.Calls()
	if len(calls) != 2 || !client.runner.SawDeadline() {
		t.Fatalf("calls = %+v", calls)
	}
	create := calls[1]
	for _, flag := range []string{"--no-start", "--no-deps", "--no-recreate", "--no-build", "never"} {
		if !slices.Contains(create.Args, flag) {
			t.Errorf("missing %s: %v", flag, create.Args)
		}
	}
	if strings.Contains(strings.Join(create.Env, "\n"), "wrong-daemon") || slices.Contains(create.Env, "COMPOSE_REMOVE_ORPHANS=1") {
		t.Fatalf("ambient environment leaked: %v", create.Env)
	}
	var model map[string]any
	if err := json.Unmarshal([]byte(create.Input), &model); err != nil {
		t.Fatal(err)
	}
	services := model["services"].(map[string]any)
	if len(services) != 1 {
		t.Fatalf("other services escaped isolation: %v", services)
	}
	service := services["app"].(map[string]any)
	if service["image"] != m.Spec.Compose.Image || service["depends_on"] != nil {
		t.Fatalf("unsafe service model: %v", service)
	}
	if service["environment"].(map[string]any)["PASSWORD"] != "$$literal$${keep}" {
		t.Fatal("resolved values must survive Compose's second interpolation")
	}
	for _, kind := range []string{"volumes", "networks"} {
		for _, raw := range model[kind].(map[string]any) {
			if raw.(map[string]any)["external"] != true {
				t.Fatalf("%s could be created empty", kind)
			}
		}
	}
	if err := m.StartCreated(t.Context(), id); err != nil || client.started != id {
		t.Fatalf("StartCreated = %v, target=%q", err, client.started)
	}
	if err := m.VerifyCreated(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyCreated(t.Context(), "replacement"); err == nil {
		t.Fatal("a different generation must not pass verification")
	}
}

func TestComposeRecreationRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Manager, *recoveryClient, map[string]any)
		want   string
		create bool
	}{
		{"no recipe", func(m *Manager, _ *recoveryClient, _ map[string]any) { m.Spec.Compose = nil }, "control.compose", false},
		{"missing source", func(m *Manager, _ *recoveryClient, _ map[string]any) {
			m.Spec.Compose.Files = []string{filepath.Join(m.Spec.Compose.Directory, "missing")}
		}, "regular file", false},
		{"permission failure", func(_ *Manager, c *recoveryClient, _ map[string]any) { c.inspectErr = errors.New("permission denied") }, "permission denied", false},
		{"missing image", func(_ *Manager, c *recoveryClient, _ map[string]any) { c.imageErr = errors.New("not found") }, "pinned image", false},
		{"missing data", func(_ *Manager, c *recoveryClient, _ map[string]any) { c.resourcesErr = errors.New("not found") }, "unavailable", false},
		{"unknown ownership", func(_ *Manager, c *recoveryClient, _ map[string]any) { c.listErr = errors.New("offline") }, "ownership", false},
		{"implicit image volume", func(_ *Manager, c *recoveryClient, _ map[string]any) { c.image.Config.Volumes["/other"] = nil }, "anonymous volume", false},
		{"concurrent creation", func(_ *Manager, c *recoveryClient, _ map[string]any) { c.concurrent = true }, "concurrently", true},
		{"wrong image created", func(_ *Manager, c *recoveryClient, _ map[string]any) { c.wrongImage = true }, "does not match", true},
		{"other replica", func(_ *Manager, c *recoveryClient, _ map[string]any) {
			c.containers = []ContainerSummary{{Names: []string{"/other"}, Labels: map[string]string{composeProjectLabel: "project", composeServiceLabel: "app"}}}
		}, "another container", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, client, model := recoveryFixture(t)
			tc.change(&m, client, model)
			setComposeOutput(t, client.runner, model)
			_, err := m.Create(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Create error = %v, want %q", err, tc.want)
			}
			created := false
			for _, call := range client.runner.Calls() {
				created = created || call.Input != ""
			}
			if created != tc.create || client.started != "" {
				t.Fatalf("created=%v started=%q", created, client.started)
			}
		})
	}
}

func TestComposeRecreatedContainerRetainsValidatedSource(t *testing.T) {
	m, client, model := recoveryFixture(t)
	if _, err := m.Create(t.Context()); err != nil {
		t.Fatal(err)
	}
	creation := client.runner.Calls()[1]
	c, err := client.Inspect(t.Context(), m.Spec.Container)
	if err != nil {
		t.Fatal(err)
	}
	c.Config.Labels[composeConfigHashLabel] = "created-hash"
	client.container = c
	spec, present, err := ComposeFromLabels(c.Config.Labels, c.Image)
	if err != nil || !present || !slices.Equal(spec.Files, m.Spec.Compose.Files) {
		t.Fatalf("rediscovery=%+v present=%v err=%v", spec, present, err)
	}
	m.Spec.Compose = &spec
	client.runner = &execxtest.Runner{RespectContext: true}
	m.Runner = client.runner
	setComposeOutput(t, client.runner, model)
	client.runner.Queue[1].Stdout = "app created-hash\n"
	if err := m.ValidateCompose(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	calls := client.runner.Calls()
	if len(calls) != 2 || calls[1].Input != creation.Input || !slices.Contains(calls[1].Args, "--hash") || client.started != "" {
		t.Fatalf("rediscovery did not validate the original isolated definition: %+v", calls)
	}
	if _, _, err := ComposeFromLabels(c.Config.Labels, "different-image"); err == nil {
		t.Fatal("retained source accepted a different image")
	}
}

func TestComposeModelRefusals(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value any
	}{
		{"container_name", "another"}, {"scale", 2}, {"deploy", map[string]any{"replicas": 2}},
		{"deploy", map[string]any{"mode": "global"}}, {"post_start", []any{map[string]any{"command": "danger"}}},
		{"env_file", []any{"unresolved.env"}}, {"network_mode", "service:db"},
		{"use_api_socket", true},
		{"volumes", []any{map[string]any{"type": "volume", "target": "/data"}}},
		{"volumes", []any{map[string]any{"type": "bind", "source": "/definitely-missing-sermo-test", "target": "/data"}}},
		{"secrets", []any{map[string]any{"source": "missing"}}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			m, client, model := recoveryFixture(t)
			if tc.key == "volumes" {
				mount := tc.value.([]any)[0].(map[string]any)
				if mount["type"] == "bind" {
					mount["source"] = filepath.Join(t.TempDir(), "missing")
				}
			}
			model["services"].(map[string]any)["app"].(map[string]any)[tc.key] = tc.value
			setComposeOutput(t, client.runner, model)
			if _, err := m.Create(t.Context()); err == nil {
				t.Fatal("unsafe definition accepted")
			}
			if len(client.runner.Calls()) != 1 {
				t.Fatalf("creation ran on a refused definition: %v", client.runner.Calls())
			}
		})
	}
}

func TestComposeRegistrationRequiresMatchingSource(t *testing.T) {
	for _, hash := range []string{"matching", "different", ""} {
		t.Run(hash, func(t *testing.T) {
			m, c, model := recoveryFixture(t)
			c.container = Container{ID: "existing", Image: c.image.ID, Config: ContainerConfig{Labels: map[string]string{
				composeProjectLabel: "project", composeServiceLabel: "app", composeConfigHashLabel: hash,
			}}}
			body, err := json.Marshal(model)
			if err != nil {
				t.Fatal(err)
			}
			c.runner.Queue = []execx.Result{{Stdout: "app matching\n"}, {Stdout: strings.ReplaceAll(string(body), "$", "$$")}}
			err = m.ValidateCompose(t.Context(), nil)
			if (err == nil) != (hash == "matching") {
				t.Fatalf("ValidateCompose = %v", err)
			}
			for _, call := range c.runner.Calls() {
				if slices.Contains(call.Args, "up") || call.Input != "" {
					t.Fatal("registration mutated Docker")
				}
			}
		})
	}
}

func TestComposeIdentityRequiredAtEveryPhase(t *testing.T) {
	for _, field := range []string{composeProjectLabel, composeServiceLabel, ComposeKeyImage, "missing labels"} {
		for _, phase := range []string{"discovery", "registration", "start"} {
			t.Run(field+"/"+phase, func(t *testing.T) {
				m, c, _ := recoveryFixture(t)
				source, err := json.Marshal(m.Spec.Compose)
				if err != nil {
					t.Fatal(err)
				}
				labels := map[string]string{
					composeProjectLabel: "project", composeServiceLabel: "app",
					composeSourceLabel: string(source), composeRecoveryLabel: "token",
				}
				c.container = Container{ID: "created-id", Name: "/web", Image: c.image.ID, Config: ContainerConfig{Labels: labels}, State: ContainerState{Status: ContainerStatusCreated}}
				switch field {
				case ComposeKeyImage:
					c.container.Image = "different-image"
				case "missing labels":
					c.container.Config.Labels = nil
				default:
					labels[field] = "different"
				}
				switch phase {
				case "discovery":
					_, present, discoveryErr := ComposeFromLabels(c.container.Config.Labels, c.container.Image)
					if discoveryErr == nil && present {
						t.Fatal("discovery accepted a mismatched identity")
					}
				case "registration":
					if err := m.ValidateCompose(t.Context(), nil); err == nil {
						t.Fatal("registration accepted a mismatched identity")
					}
				case "start":
					if err := m.StartCreated(t.Context(), c.container.ID); err == nil {
						t.Fatal("start accepted a mismatched identity")
					}
				}
				if c.started != "" || len(c.runner.Calls()) != 0 {
					t.Fatal("an identity mismatch must stop before commands or start")
				}
			})
		}
	}
}

func TestComposeBindSecretsAndDiscovery(t *testing.T) {
	m, c, model := recoveryFixture(t)
	service := model["services"].(map[string]any)["app"].(map[string]any)
	data := filepath.Join(m.Spec.Compose.Directory, "$data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	service["volumes"] = []any{map[string]any{"type": "bind", "source": data, "target": "/data"}}
	service["secrets"] = []any{map[string]any{"source": "password"}}
	model["secrets"] = map[string]any{"password": map[string]any{"file": m.Spec.Compose.Files[0]}}
	setComposeOutput(t, c.runner, model)
	if _, err := m.Create(t.Context()); err != nil {
		t.Fatal(err)
	}
	input := c.runner.Calls()[1].Input
	if !strings.Contains(input, `"create_host_path":false`) {
		t.Fatal("bind creation was not disabled")
	}
	labels := map[string]string{composeProjectLabel: "project", composeServiceLabel: "app", composeDirectoryLabel: m.Spec.Compose.Directory, composeFilesLabel: strings.Join(m.Spec.Compose.Files, ",")}
	for _, oneoff := range []bool{false, true} {
		if oneoff {
			labels[composeOneoffLabel] = "True"
		}
		spec, present, err := ComposeFromLabels(labels, c.image.ID)
		if err != nil || present == oneoff {
			t.Fatalf("discovery=%+v %v %v", spec, present, err)
		}
	}
}

func TestComposeCreationErrorsAndCancellation(t *testing.T) {
	m, c, _ := recoveryFixture(t)
	c.runner.Queue[1] = execx.Result{ExitCode: 1, Stderr: "PASSWORD=secret"}
	id, err := m.Create(t.Context())
	if err == nil || id != "created-id" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("partial creation = %q, %v", id, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m, c, _ = recoveryFixture(t)
	if _, err := m.Create(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled create = %v", err)
	}
	if c.started != "" {
		t.Fatal("cancelled creation started a container")
	}
}
