package dockerctl

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/hostfs"
)

const (
	composeKeyName     = "name"
	composeKeySource   = "source"
	composeKeyTarget   = "target"
	composeKeyType     = "type"
	composeKeyFile     = "file"
	composeKeyExternal = "external"
	composeKeyImage    = "image"
	composeKeyScale    = "scale"
	composeKeyDeploy   = "deploy"
	composeKeyReplicas = "replicas"
	composeVolumeBind  = "bind"
	composeVolumeNamed = "volume"
	composeVolumeTmpfs = "tmpfs"
)

// Image reads the local image; recovery never pulls or builds it.
func (c *Client) Image(ctx context.Context, id string) (Image, error) {
	var image Image
	err := c.get(ctx, "/images/"+url.PathEscape(id)+"/json", dockerResponseBodyLimit, &image)
	return image, err
}

// Resource verifies an existing network or volume without creating it.
func (c *Client) Resource(ctx context.Context, kind, name string) error {
	if kind != composeKeyVolumes && kind != composeKeyNetworks {
		return fmt.Errorf("unsupported Docker resource %q", kind)
	}
	var resource map[string]any
	return c.get(ctx, "/"+kind+"/"+url.PathEscape(name), dockerResponseBodyLimit, &resource)
}

func (m Manager) isolateCompose(ctx context.Context, client composeClient, model map[string]any, containers []ContainerSummary) error {
	spec := m.Spec.Compose
	services, _ := model[composeKeyServices].(map[string]any)
	service, ok := services[spec.Service].(map[string]any)
	if !ok {
		return fmt.Errorf("compose service %q is not defined", spec.Service)
	}
	if err := m.checkComposeService(service); err != nil {
		return err
	}
	if err := m.checkComposeReplicas(ctx, client, containers); err != nil {
		return err
	}
	image, err := client.Image(ctx, spec.Image)
	if err != nil {
		return fmt.Errorf("required pinned image is unavailable: %w", err)
	}
	if image.ID != spec.Image {
		return errors.New("docker returned a different image identity")
	}
	volumes, err := checkComposeMounts(ctx, client, service, model, image)
	if err != nil {
		return err
	}
	networks, err := checkComposeNetworks(ctx, client, service, model)
	if err != nil {
		return err
	}
	resources := map[string]any{}
	for _, kind := range []string{composeKeySecrets, composeKeyConfigs} {
		files, err := composeFileResources(service, model, kind)
		if err != nil {
			return err
		}
		resources[kind] = files
	}
	clear(model)
	model[composeKeyName] = spec.Project
	model[composeKeyServices] = map[string]any{spec.Service: service}
	model[composeKeyVolumes], model[composeKeyNetworks] = volumes, networks
	maps.Copy(model, resources)
	service[composeKeyContainerName], service[composeKeyImage] = m.Spec.Container, spec.Image
	service["pull_policy"] = "never"
	// Dependencies are observed through normal checks. They are never created
	// or started as a side effect of recovering this one service.
	for _, key := range []string{"build", "depends_on", "profiles"} {
		delete(service, key)
	}
	return nil
}

func (m Manager) checkComposeService(service map[string]any) error {
	if name := cfgval.String(service[composeKeyContainerName]); name != "" && name != m.Spec.Container {
		return errors.New("compose container_name differs from control.container")
	}
	if enabled, _ := service["use_api_socket"].(bool); enabled {
		return errors.New("compose recovery requires explicit mounts instead of use_api_socket")
	}
	for _, key := range []string{"pre_start", "post_start", "pre_stop", "provider", "volumes_from", "links", "external_links", "env_file", "develop", "models"} {
		if value, exists := service[key]; exists && value != nil {
			return fmt.Errorf("compose recovery cannot isolate service field %s", key)
		}
	}
	for _, key := range []string{"network_mode", "ipc", "pid"} {
		value := cfgval.String(service[key])
		if strings.HasPrefix(value, "service:") || strings.HasPrefix(value, "container:") {
			return fmt.Errorf("compose recovery cannot isolate %s=%s", key, value)
		}
	}
	if replicas, ok := service[composeKeyScale]; ok && cfgval.String(replicas) != "1" {
		return errors.New("compose recovery requires exactly one container per service")
	}
	if deploy, ok := service[composeKeyDeploy].(map[string]any); ok {
		if replicas, ok := deploy[composeKeyReplicas]; ok && cfgval.String(replicas) != "1" {
			return errors.New("compose recovery requires exactly one replica")
		}
		if mode := cfgval.String(deploy["mode"]); mode != "" && mode != "replicated" {
			return errors.New("compose recovery does not manage jobs or global services")
		}
	}
	return nil
}

func (m Manager) checkComposeReplicas(ctx context.Context, client composeClient, containers []ContainerSummary) error {
	if containers == nil {
		var err error
		containers, err = client.ListContainers(ctx, true)
		if err != nil {
			return fmt.Errorf("check Compose service ownership: %w", err)
		}
	}
	for _, c := range containers {
		if c.Labels[composeProjectLabel] != m.Spec.Compose.Project || c.Labels[composeServiceLabel] != m.Spec.Compose.Service {
			continue
		}
		if len(c.Names) != 1 || strings.TrimPrefix(c.Names[0], "/") != m.Spec.Container || strings.EqualFold(c.Labels[composeOneoffLabel], "true") {
			return errors.New("another container belongs to this Compose service; refusing duplicate or scaled recovery")
		}
	}
	return nil
}

func checkComposeMounts(ctx context.Context, client composeClient, service, model map[string]any, image Image) (map[string]any, error) {
	external := map[string]any{}
	targets := map[string]bool{}
	mounts, _ := service[composeKeyVolumes].([]any)
	for _, raw := range mounts {
		mount, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("compose returned a non-canonical volume")
		}
		targets[cfgval.String(mount[composeKeyTarget])] = true
		source := cfgval.String(mount[composeKeySource])
		switch cfgval.String(mount[composeKeyType]) {
		case composeVolumeBind:
			if !cleanAbsolute(source) {
				return nil, errors.New("compose bind source must be an absolute clean path")
			}
			if _, err := hostfs.StatTarget(source); err != nil {
				return nil, fmt.Errorf("required bind source is unavailable: %w", err)
			}
			options, _ := mount[composeVolumeBind].(map[string]any)
			if options == nil {
				options = map[string]any{}
			}
			options["create_host_path"] = false
			mount[composeVolumeBind] = options
		case composeVolumeNamed:
			if err := externalResource(ctx, client, model, external, composeKeyVolumes, source); err != nil {
				return nil, err
			}
		case composeVolumeTmpfs:
		default:
			return nil, errors.New("compose recovery requires bind, named volume or tmpfs mounts")
		}
	}
	for target := range image.Config.Volumes {
		if !targets[target] {
			return nil, fmt.Errorf("image volume %s has no explicit persistent source; refusing an empty anonymous volume", target)
		}
	}
	return external, nil
}

func checkComposeNetworks(ctx context.Context, client composeClient, service, model map[string]any) (map[string]any, error) {
	external := map[string]any{}
	networks, _ := service[composeKeyNetworks].(map[string]any)
	if len(networks) == 0 && cfgval.String(service["network_mode"]) == "" {
		return nil, errors.New("compose returned no explicit network; refusing creation of an implicit default network")
	}
	for key := range networks {
		if err := externalResource(ctx, client, model, external, composeKeyNetworks, key); err != nil {
			return nil, err
		}
	}
	return external, nil
}

func externalResource(ctx context.Context, client composeClient, model, out map[string]any, kind, key string) error {
	definitions, _ := model[kind].(map[string]any)
	definition, _ := definitions[key].(map[string]any)
	name := cfgval.String(definition[composeKeyName])
	if key == "" || name == "" {
		return fmt.Errorf("compose %s requires an explicit existing resource name", kind)
	}
	if err := client.Resource(ctx, kind, name); err != nil {
		return fmt.Errorf("required %s %q is unavailable: %w", kind, name, err)
	}
	out[key] = map[string]any{composeKeyExternal: true, composeKeyName: name}
	return nil
}

func composeFileResources(service, model map[string]any, kind string) (map[string]any, error) {
	out := map[string]any{}
	refs, _ := service[kind].([]any)
	definitions, _ := model[kind].(map[string]any)
	for _, raw := range refs {
		ref, _ := raw.(map[string]any)
		key := cfgval.String(ref[composeKeySource])
		definition, _ := definitions[key].(map[string]any)
		path := cfgval.String(definition[composeKeyFile])
		if !cleanAbsolute(path) {
			return nil, fmt.Errorf("compose %s %q requires a local file", kind, key)
		}
		info, err := hostfs.StatTarget(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("required %s file is unavailable: %s", kind, path)
		}
		out[key] = definition
	}
	return out, nil
}
