package config

import "testing"

// Container runtimes own delegated process trees (shims, proxies and workload
// descendants) whose continued presence is not a failed daemon stop. Their
// catalog profiles therefore delegate restart atomically to the init backend
// instead of treating those workload processes as residuals between Stop and
// Start. Ordinary multi-process daemons keep the staged default.
func TestDelegatedRuntimeCatalogUsesNativeRestart(t *testing.T) {
	t.Parallel()

	for _, backend := range []string{"systemd", "openrc"} {
		for _, service := range []string{"containerd", "docker"} {
			t.Run(backend+"/"+service, func(t *testing.T) {
				t.Parallel()

				resolved := resolveCatalogService(t, service, backend)
				got, err := ParseRestartMode(resolved.Tree)
				if err != nil {
					t.Fatalf("ParseRestartMode(%s): %v", service, err)
				}
				if got != RestartModeNative {
					t.Fatalf("ParseRestartMode(%s) = %q, want %q", service, got, RestartModeNative)
				}
			})
		}
	}
}

func TestDockerCatalogKeepsSystemdSocketForExplicitStartStop(t *testing.T) {
	t.Parallel()

	resolved := resolveCatalogService(t, "docker", "systemd")
	units := AdditionalUnits(resolved.Tree, "systemd")
	if len(units) != 1 || units[0] != "docker.socket" {
		t.Fatalf("AdditionalUnits(docker) = %v, want [docker.socket]", units)
	}
}
