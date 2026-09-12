package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"sermo/internal/cfgval"
)

func TestResolveServicesKeepsOrderErrorsAndIndependentTrees(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml":               baseGlobal,
		"catalog/apps/shared.yml": "name: shared\npreflight:\n  health: {type: command, command: [/bin/echo, original]}\n",
		"services/a.yml":          "name: a\naliases: [alias-a]\napps: [shared]\n",
		"services/b.yml":          "name: b\nclone: a\n",
		"services/off.yml":        "name: off\nenabled: false\n",
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"b", "missing", "alias-a"}
	batch := cfg.ResolveServices(names)
	if len(batch) != len(names) {
		t.Fatalf("batch size = %d", len(batch))
	}
	for i, name := range names {
		resolved, errs := cfg.Resolve(name)
		if !reflect.DeepEqual(batch[i].Resolved, resolved) || !slices.Equal(batch[i].Errors, errs) {
			t.Fatalf("batch differs from Resolve(%q): %+v", name, batch[i])
		}
	}
	first := nested(t, batch[0].Resolved.Tree, "preflight", "shared-health")
	first["command"].([]any)[1] = "changed"
	other := nested(t, batch[2].Resolved.Tree, "preflight", "shared-health")
	if got := cfgval.StringList(other["command"]); !slices.Equal(got, []string{"/bin/echo", "original"}) {
		t.Fatalf("batch trees share command values: %v", got)
	}
	cfg.Services["nil-document"] = nil
	if got := cfg.EnabledServiceNames(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("enabled service names = %v", got)
	}
	if got := cfg.ResolveServices(nil); len(got) != 0 {
		t.Fatalf("empty batch = %v", got)
	}
}

func TestResolveServicesRefreshesFileAndEnvironmentBetweenBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.conf")
	if err := os.WriteFile(path, []byte("port 1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERMO_BATCH_MARKER", "first")
	global := writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/shared.yml": fmt.Sprintf(`name: shared
variables:
  config: %q
  port: {from_file: "${config}", directive: port, default: 1000}
  marker: "${env:SERMO_BATCH_MARKER}"
preflight:
  health: {type: command, command: [/bin/echo, "${port}", "${marker}"]}
`, path),
		"services/a.yml": "name: a\napps: [shared]\n",
		"services/b.yml": "name: b\napps: [shared]\n",
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatal(err)
	}
	first := cfg.ResolveServices(cfg.SortedServiceNames())
	if err := os.WriteFile(path, []byte("port 5678\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERMO_BATCH_MARKER", "second")
	second := cfg.ResolveServices(cfg.SortedServiceNames())
	for i := range first {
		for j, batch := range [][]ServiceResolution{first, second} {
			if len(batch[i].Errors) != 0 {
				t.Fatal(batch[i].Errors)
			}
			got := cfgval.StringList(nested(t, batch[i].Resolved.Tree, "preflight", "shared-health")["command"])
			want := [][]string{{"/bin/echo", "1234", "first"}, {"/bin/echo", "5678", "second"}}[j]
			if !slices.Equal(got, want) {
				t.Fatalf("batch %d service %d: command = %v, want %v", j, i, got, want)
			}
		}
	}
}
