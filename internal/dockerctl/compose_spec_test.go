package dockerctl

import (
	"maps"
	"strings"
	"testing"
)

func TestComposeSpecValidation(t *testing.T) {
	base := map[string]any{
		"project": "project", "directory": "/srv/project", "files": []any{"/srv/project/compose.yml"},
		"service": "app", "image": "sha256:" + strings.Repeat("a", 64),
	}
	for _, tc := range []struct {
		key   string
		value any
		bad   bool
	}{
		{"project", "valid-project", false}, {"project", "../wrong", true},
		{"directory", "/srv/../tmp", true}, {"service", "--all", true},
		{"image", "app:latest", true}, {"image", "sha256:short", true},
		{"files", "compose.yml", true}, {"files", []any{}, true},
		{"files", []any{"/srv/project/compose.yml", ""}, true},
		{"files", []any{"/srv/project/compose.yml", 7}, true},
		{"env_files", []any{"/srv/project/.env"}, false}, {"env_files", []any{"relative"}, true},
		{"unknown", true, true},
	} {
		t.Run(tc.key, func(t *testing.T) {
			compose := maps.Clone(base)
			compose[tc.key] = tc.value
			spec, _, err := SpecFromTree(dockerControl(map[string]any{ControlKeyContainer: "web", ControlKeyCompose: compose}))
			if (err != nil) != tc.bad {
				t.Fatalf("spec=%+v error=%v", spec, err)
			}
			if err == nil && spec.Compose == nil {
				t.Fatal("recipe was discarded")
			}
		})
	}
	if _, _, err := SpecFromTree(dockerControl(map[string]any{ControlKeyContainer: "web", ControlKeyHost: "remote.example", ControlKeyCompose: base})); err == nil {
		t.Fatal("remote daemon accepted local path validation")
	}
}
