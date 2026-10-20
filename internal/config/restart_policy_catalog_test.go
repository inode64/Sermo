package config

import (
	"testing"
)

func TestDockerCatalogKeepsSystemdSocketForExplicitStartStop(t *testing.T) {
	t.Parallel()

	resolved := resolveCatalogService(t, "docker", backendSystemd)
	units := AdditionalUnits(resolved.Tree, backendSystemd)
	if len(units) != 1 || units[0] != "docker.socket" {
		t.Fatalf("AdditionalUnits(docker) = %v, want [docker.socket]", units)
	}
}
