package config

import (
	"slices"
	"testing"
)

func TestNormalizeServiceUnits(t *testing.T) {
	got := normalizeServiceUnits([]string{
		" nginx.service ",
		"",
		"nginx.service",
		"\tphp-fpm.service\n",
		"php-fpm.service",
	})
	want := []string{"nginx.service", "php-fpm.service"}
	if !slices.Equal(got, want) {
		t.Fatalf("normalizeServiceUnits = %v, want %v", got, want)
	}
}

func TestAdditionalUnitsAndValidation(t *testing.T) {
	tree := map[string]any{
		"service":      map[string]any{"systemd": []any{"docker"}, "openrc": []any{"docker"}},
		"also_service": map[string]any{"systemd": []any{"docker.socket"}},
	}
	if got := AdditionalUnits(tree, "systemd"); len(got) != 1 || got[0] != "docker.socket" {
		t.Fatalf("AdditionalUnits systemd = %v, want [docker.socket]", got)
	}
	if got := AdditionalUnits(tree, "openrc"); len(got) != 0 {
		t.Fatalf("AdditionalUnits openrc = %v, want empty", got)
	}
}
