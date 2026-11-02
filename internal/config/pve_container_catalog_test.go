package config

import "testing"

// The tree under lxc-start is the container's workload, so the fds sensor,
// which reports the process nearest its own open-files limit, says nothing
// about the container service: a PostgreSQL backend inside one caches files
// up to max_files_per_process and sat at 989 of its 1024 limit.
func TestCatalogPVEContainerHasNoFDsSensor(t *testing.T) {
	resolved := resolveCatalogService(t, "pve-container-101", backendSystemd)
	if _, ok := nested(t, resolved.Tree, "checks")[fdsCheckName]; ok {
		t.Fatal("pve-container must not get the fds check")
	}
	if rulesTree, _ := resolved.Tree["rules"].(map[string]any); rulesTree != nil {
		if _, ok := rulesTree[fdsRuleName]; ok {
			t.Fatal("pve-container must not get the fds rule")
		}
	}
}
