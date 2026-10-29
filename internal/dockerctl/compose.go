package dockerctl

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"sermo/internal/execx"
	"sermo/internal/hostfs"
	"sermo/internal/servicemgr"
)

const (
	composeCommand          = "docker"
	composeModelLimit       = 8 << 20
	composeKeyServices      = "services"
	composeKeyContainerName = "container_name"
	composeKeyLabels        = "labels"
	composeKeyVolumes       = "volumes"
	composeKeyNetworks      = "networks"
	composeKeySecrets       = "secrets"
	composeKeyConfigs       = "configs"
)

// composeClient adds the read-only resource queries required before creation.
type composeClient interface {
	DockerClient
	ListContainers(ctx context.Context, all bool) ([]ContainerSummary, error)
	Image(ctx context.Context, id string) (Image, error)
	Resource(ctx context.Context, kind, name string) error
}

// Create restores only a missing configured container, leaving it stopped for
// internal/operation to verify and start through its ordinary lifecycle.
func (m Manager) Create(ctx context.Context) (string, error) {
	ctx, cancel := ensureDeadline(ctx)
	defer cancel()
	client, err := m.client()
	if err != nil {
		return "", err
	}
	defer client.CloseIdleConnections()
	if _, err := client.Inspect(ctx, m.Spec.Container); !errors.Is(err, servicemgr.ErrNotFound) {
		if err == nil {
			err = errors.New("container appeared before creation; retry with its current state")
		}
		return "", err
	}
	model, err := m.recoveryModel(ctx, client)
	if err != nil {
		return "", err
	}
	token := rand.Text()
	input, err := m.composeInput(model, token)
	if err != nil {
		return "", err
	}
	// The exact model just validated is sent on stdin. Re-reading the source
	// files here could apply different mounts, hooks or variables after validation.
	_, createErr := m.runCompose(ctx, input, "up", "--no-start", "--no-deps", "--no-recreate", "--no-build", "--pull", "never", "--", m.Spec.Compose.Service)
	container, inspectErr := client.Inspect(ctx, m.Spec.Container)
	if inspectErr != nil {
		return "", errors.Join(createErr, fmt.Errorf("inspect created container: %w", inspectErr))
	}
	if container.Config.Labels[composeRecoveryLabel] != token {
		return "", errors.Join(createErr, errors.New("container appeared concurrently; its identity does not belong to this creation attempt"))
	}
	if err := m.verifyCreated(container); err != nil {
		return container.ID, errors.Join(createErr, err)
	}
	return container.ID, createErr
}

func (m Manager) composeInput(model map[string]any, token string) (string, error) {
	services, _ := model[composeKeyServices].(map[string]any)
	service, _ := services[m.Spec.Compose.Service].(map[string]any)
	if service == nil {
		return "", errors.New("validated Compose service is missing")
	}
	labels, _ := service[composeKeyLabels].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	labels[composeRecoveryLabel] = token
	// Compose records stdin as its source when applying the isolated model.
	// Keep the original recipe so later discovery can validate it again.
	source, err := json.Marshal(m.Spec.Compose)
	if err != nil {
		return "", fmt.Errorf("encode Compose source: %w", err)
	}
	labels[composeSourceLabel] = string(source)
	service[composeKeyLabels] = labels
	input, err := json.Marshal(escapeComposeValues(model))
	if err != nil {
		return "", fmt.Errorf("encode recovery definition: %w", err)
	}
	return string(input), nil
}

func (m Manager) verifyCreated(c Container) error {
	if c.ID == "" || c.ContainerName() != m.Spec.Container || !m.Spec.Compose.matchesIdentity(c.Image, c.Config.Labels) {
		return errors.New("created container does not match the configured name, image, project and service")
	}
	if c.State.Status != ContainerStatusCreated || c.State.Running || c.State.Pid != 0 {
		return errors.New("created container is not stopped; refusing to claim or restart an unexpected running generation")
	}
	return nil
}

// StartCreated binds the start to the ID returned by this creation, so a
// concurrent deletion/name reuse cannot start a different container.
func (m Manager) StartCreated(ctx context.Context, id string) error {
	return m.withClient(func(client DockerClient) error {
		container, err := client.Inspect(ctx, id)
		if err != nil {
			return fmt.Errorf("inspect created container before start: %w", err)
		}
		if container.ID != id {
			return errors.New("created container identity changed")
		}
		if err := m.verifyCreated(container); err != nil {
			return err
		}
		return client.Start(ctx, id)
	})
}

// VerifyCreated rejects a replacement generation even if it is already healthy.
func (m Manager) VerifyCreated(ctx context.Context, id string) error {
	c, err := m.inspect(ctx)
	if err != nil {
		return err
	}
	if c.ID != id || statusFromContainer(c) != servicemgr.StatusActive {
		return errors.New("created container is no longer the active configured target")
	}
	return nil
}

// ValidateCompose verifies a discovered recipe without creating or starting it.
// Registration and recreation share this validation; labels alone are not proof
// that the source files and persistent data are still available.
func (m Manager) ValidateCompose(ctx context.Context) error {
	ctx, cancel := ensureDeadline(ctx)
	defer cancel()
	client, err := m.client()
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	if m.Spec.Compose == nil {
		return errors.New("compose source is not configured")
	}
	container, err := client.Inspect(ctx, m.Spec.Container)
	if err != nil {
		return fmt.Errorf("inspect container before registering recovery: %w", err)
	}
	if !m.Spec.Compose.matchesIdentity(container.Image, container.Config.Labels) {
		return errors.New("container does not match the discovered Compose project, service and image")
	}
	labels := container.Config.Labels
	if labels[composeSourceLabel] != "" {
		model, err := m.recoveryModel(ctx, client)
		if err != nil {
			return err
		}
		input, err := m.composeInput(model, labels[composeRecoveryLabel])
		if err != nil {
			return err
		}
		return m.verifyComposeSource(ctx, input, labels[composeConfigHashLabel])
	}
	if err := m.verifyComposeSource(ctx, "", labels[composeConfigHashLabel]); err != nil {
		return err
	}
	_, err = m.recoveryModel(ctx, client)
	return err
}

func (m Manager) verifyComposeSource(ctx context.Context, input, expected string) error {
	if expected == "" {
		return errors.New("container has no Compose configuration hash; supply and verify its creation definition explicitly")
	}
	out, err := m.runCompose(ctx, input, "config", "--hash", m.Spec.Compose.Service)
	if err != nil {
		return err
	}
	fields := strings.Fields(out)
	if len(fields) != 2 || fields[0] != m.Spec.Compose.Service || fields[1] != expected {
		return errors.New("compose source differs from the container; check overrides and environment files before registering recreation")
	}
	return nil
}

func (m Manager) recoveryModel(ctx context.Context, client DockerClient) (map[string]any, error) {
	if m.Spec.Compose == nil {
		return nil, errors.New("container does not exist; configure control.compose to permit recreation")
	}
	if err := m.Spec.Compose.Validate(); err != nil {
		return nil, err
	}
	if m.Spec.Socket == "" {
		return nil, errors.New("compose recovery requires a local Docker socket")
	}
	if err := m.Spec.Compose.checkFiles(); err != nil {
		return nil, err
	}
	out, err := m.runCompose(ctx, "", "config", "--format", "json", "--", m.Spec.Compose.Service)
	if err != nil {
		return nil, err
	}
	if len(out) > composeModelLimit {
		return nil, errors.New("resolved Compose definition exceeds size limit")
	}
	// `compose config` doubles every dollar for reuse as Compose input, even
	// in JSON. Validate real paths/values, then escape once when sending stdin.
	out = strings.ReplaceAll(out, "$$", "$")
	var model map[string]any
	if err := json.Unmarshal([]byte(out), &model); err != nil {
		return nil, errors.New("compose returned an invalid resolved JSON definition")
	}
	resources, ok := client.(composeClient)
	if !ok {
		return nil, errors.New("docker client cannot verify recovery resources")
	}
	if err := m.isolateCompose(ctx, resources, model); err != nil {
		return nil, err
	}
	return model, nil
}

func (s ComposeSpec) checkFiles() error {
	info, err := hostfs.StatTarget(s.Directory)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%s.directory is unavailable: %s", composePath, s.Directory)
	}
	for _, file := range append(append([]string{}, s.Files...), s.EnvFiles...) {
		info, err := hostfs.StatTarget(file)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("compose source is not an available regular file: %s", file)
		}
	}
	return nil
}

func (m Manager) runCompose(ctx context.Context, input string, command ...string) (string, error) {
	if len(command) == 0 {
		return "", errors.New("compose command is required")
	}
	runner, ok := execx.RunnerOrDefault(m.Runner).(execx.InputEnvRunner)
	if !ok {
		return "", errors.New("compose runner cannot supply a controlled environment and stdin")
	}
	spec := m.Spec.Compose
	args := []string{"--host", "unix://" + m.Spec.Socket, "compose", "--ansi", "never", "--project-name", spec.Project, "--project-directory", spec.Directory}
	if input == "" {
		for _, file := range spec.Files {
			args = append(args, "--file", file)
		}
		for _, file := range spec.EnvFiles {
			args = append(args, "--env-file", file)
		}
	} else {
		args = append(args, "--file", "-", "--env-file", "/dev/null")
	}
	args = append(args, command...)
	// No ambient DOCKER_CONTEXT, COMPOSE_* or interpolation variables may
	// redirect the operation, remove orphans, or change the validated recipe.
	env := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/", "COMPOSE_DISABLE_ENV_FILE=1", "COMPOSE_REMOVE_ORPHANS=0", "COMPOSE_IGNORE_ORPHANS=1"}
	res, err := runner.RunInputEnv(ctx, input, env, composeCommand, args...)
	if err != nil || res.ExitCode != execx.ExitCodeSuccess {
		if ctx.Err() != nil {
			return "", fmt.Errorf("compose %s: %w", command[0], ctx.Err())
		}
		// Compose diagnostics can echo interpolated credentials. Only the phase
		// and exit status are safe to persist in Sermo's shared event history.
		return "", fmt.Errorf("compose %s failed (exit %d); validate the configured files with docker compose config", command[0], res.ExitCode)
	}
	return res.Stdout, nil
}

func escapeComposeValues(value any) any {
	switch v := value.(type) {
	case string:
		return strings.ReplaceAll(v, "$", "$$")
	case map[string]any:
		for key, item := range v {
			v[key] = escapeComposeValues(item)
		}
	case []any:
		for i, item := range v {
			v[i] = escapeComposeValues(item)
		}
	}
	return value
}

// ComposeFromLabels locates the creation source while the container still
// exists. The caller must ValidateCompose before offering it for registration.
func ComposeFromLabels(labels map[string]string, image string) (ComposeSpec, bool, error) {
	if labels[composeProjectLabel] == "" || strings.EqualFold(labels[composeOneoffLabel], "true") {
		return ComposeSpec{}, false, nil
	}
	if source := labels[composeSourceLabel]; source != "" {
		var spec ComposeSpec
		if err := json.Unmarshal([]byte(source), &spec); err != nil {
			return ComposeSpec{}, true, errors.New("invalid retained Compose source metadata")
		}
		if err := spec.Validate(); err != nil {
			return ComposeSpec{}, true, err
		}
		if !spec.matchesIdentity(image, labels) || labels[composeRecoveryLabel] == "" {
			return ComposeSpec{}, true, errors.New("retained Compose source does not match the container")
		}
		return spec, true, nil
	}
	spec := ComposeSpec{Project: labels[composeProjectLabel], Service: labels[composeServiceLabel], Directory: labels[composeDirectoryLabel], Image: image}
	spec.Files = strings.Split(labels[composeFilesLabel], ",")
	if env := labels[composeEnvFilesLabel]; env != "" {
		spec.EnvFiles = strings.Split(env, ",")
	} else {
		path := filepath.Join(spec.Directory, ".env")
		if info, err := hostfs.StatTarget(path); err == nil && info.Mode().IsRegular() {
			spec.EnvFiles = []string{path}
		}
	}
	if err := spec.Validate(); err != nil {
		return ComposeSpec{}, true, err
	}
	return spec, true, nil
}
