package dockerctl

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"sermo/internal/cfgval"
)

// ComposeKey constants are the public control.compose vocabulary.
const (
	ComposeKeyProject      = "project"
	ComposeKeyDirectory    = "directory"
	ComposeKeyFiles        = "files"
	ComposeKeyEnvFiles     = "env_files"
	ComposeKeyService      = "service"
	ComposeKeyImage        = "image"
	composePath            = "control.compose"
	composeProjectLabel    = "com.docker.compose.project"
	composeServiceLabel    = "com.docker.compose.service"
	composeDirectoryLabel  = "com.docker.compose.project.working_dir"
	composeFilesLabel      = "com.docker.compose.project.config_files"
	composeEnvFilesLabel   = "com.docker.compose.project.environment_file"
	composeOneoffLabel     = "com.docker.compose.oneoff"
	composeConfigHashLabel = "com.docker.compose.config-hash"
	composeRecoveryLabel   = "io.sermo.recovery"
	composeSourceLabel     = "io.sermo.compose-source"
)

var (
	composeProjectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	composeServiceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
	composeImageID     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// ComposeSpec identifies the authoritative creation recipe and pins the image
// already observed on this Docker daemon. Secrets stay in the source files.
type ComposeSpec struct {
	Project   string   `json:"project"`
	Directory string   `json:"directory"`
	Files     []string `json:"files"`
	EnvFiles  []string `json:"env_files,omitempty"`
	Service   string   `json:"service"`
	Image     string   `json:"image"`
}

func (s *Spec) parseCompose(control map[string]any) error {
	raw, exists := control[ControlKeyCompose]
	if !exists {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return errors.New(composePath + " must be a mapping")
	}
	keys := []string{ComposeKeyProject, ComposeKeyDirectory, ComposeKeyFiles, ComposeKeyEnvFiles, ComposeKeyService, ComposeKeyImage}
	for key := range m {
		if !slices.Contains(keys, key) {
			return fmt.Errorf("%s.%s is not supported", composePath, key)
		}
	}
	spec := &ComposeSpec{}
	for key, target := range map[string]*string{
		ComposeKeyProject: &spec.Project, ComposeKeyDirectory: &spec.Directory,
		ComposeKeyService: &spec.Service, ComposeKeyImage: &spec.Image,
	} {
		value, ok := m[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s.%s requires a non-empty string", composePath, key)
		}
		*target = value
	}
	var err error
	if spec.Files, err = composePaths(m[ComposeKeyFiles], ComposeKeyFiles, true); err != nil {
		return err
	}
	if spec.EnvFiles, err = composePaths(m[ComposeKeyEnvFiles], ComposeKeyEnvFiles, false); err != nil {
		return err
	}
	if err := spec.Validate(); err != nil {
		return err
	}
	if s.Socket == "" {
		return errors.New(composePath + " requires a local Unix Docker socket; host paths cannot be verified for a remote daemon")
	}
	if !composeServiceName.MatchString(s.Container) {
		return errors.New("control.container must be a stable container name for Compose recovery")
	}
	s.Compose = spec
	return nil
}

func composePaths(raw any, key string, required bool) ([]string, error) {
	if raw == nil && !required {
		return nil, nil
	}
	paths, err := cfgval.StrictStringArray(raw)
	if err != nil || required && len(paths) == 0 {
		return nil, fmt.Errorf("%s.%s requires a list of absolute paths", composePath, key)
	}
	for _, path := range paths {
		if !cleanAbsolute(path) {
			return nil, fmt.Errorf("%s.%s: %q must be an absolute clean path", composePath, key, path)
		}
	}
	return paths, nil
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\x00\n\r")
}

// Validate checks the same contract for operator config and discovered recipes.
func (s ComposeSpec) Validate() error {
	if !composeProjectName.MatchString(s.Project) {
		return errors.New(composePath + ".project is not a valid Compose project name")
	}
	if !composeServiceName.MatchString(s.Service) {
		return errors.New(composePath + ".service is not a valid Compose service name")
	}
	if !cleanAbsolute(s.Directory) {
		return errors.New(composePath + ".directory must be an absolute clean path")
	}
	if len(s.Files) == 0 {
		return errors.New(composePath + ".files requires at least one Compose file")
	}
	for _, path := range append(slices.Clone(s.Files), s.EnvFiles...) {
		if !cleanAbsolute(path) {
			return fmt.Errorf("%s: %q must be an absolute clean path", composePath, path)
		}
	}
	if !composeImageID.MatchString(s.Image) {
		return errors.New(composePath + ".image must be the observed sha256 image ID (64 lowercase hex digits)")
	}
	return nil
}

// Tree is the single YAML shape shared by the wizard and its validations.
func (s ComposeSpec) Tree() map[string]any {
	tree := map[string]any{
		ComposeKeyProject: s.Project, ComposeKeyDirectory: s.Directory,
		ComposeKeyFiles: s.Files, ComposeKeyService: s.Service, ComposeKeyImage: s.Image,
	}
	if len(s.EnvFiles) > 0 {
		tree[ComposeKeyEnvFiles] = s.EnvFiles
	}
	return tree
}
