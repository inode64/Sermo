package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// TestDocsSermoAllValidates loads docs/sermo-all.yml — the complete annotated
// configuration example — through the real loader and validator, so the
// reference file cannot drift from the schema. Each `---` document is placed
// where it would live in a deployment: the global part as sermo.yml, catalog
// kinds (daemon/app/lib/patterns) in a catalog dir, services in an include
// dir, mounts in a mounts dir; the example's absolute paths are rewritten to
// the test sandbox.
func TestDocsSermoAllValidates(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "docs", "sermo-all.yml"))
	if err != nil {
		t.Skipf("docs/sermo-all.yml not found: %v", err)
	}

	dir := t.TempDir()
	catalogExtra := filepath.Join(dir, "catalog-extra")
	enabled := filepath.Join(dir, "enabled")
	mountsDir := filepath.Join(dir, "mounts")
	// The loader classifies catalog entries by subdirectory, mirroring the
	// packaged layout (catalog/{services,apps,libs,patterns}).
	subdir := map[string]string{"daemon": "services", "app": "apps", "lib": "libs", "patterns": "patterns"}
	for _, d := range []string{enabled, catalogExtra, mountsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, sub := range subdir {
		if err := os.MkdirAll(filepath.Join(catalogExtra, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	var globalDoc string
	var services []string
	var mounts []string
	for i, doc := range strings.Split(string(raw), "\n---\n") {
		var body map[string]any
		if err := yaml.Unmarshal([]byte(doc), &body); err != nil {
			t.Fatalf("document %d does not parse: %v", i+1, err)
		}
		kind, _ := body["kind"].(string)
		name, _ := body["name"].(string)
		switch kind {
		case "":
			if globalDoc != "" {
				t.Fatalf("document %d: second global (kind-less) document", i+1)
			}
			globalDoc = doc
		case "daemon", "app", "lib", "patterns":
			if err := os.WriteFile(filepath.Join(catalogExtra, subdir[kind], name+".yml"), []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
		case "service":
			services = append(services, name)
			if err := os.WriteFile(filepath.Join(enabled, name+".yml"), []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
		case "mount":
			mounts = append(mounts, name)
			if err := os.WriteFile(filepath.Join(mountsDir, name+".yml"), []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("document %d: unknown kind %q", i+1, kind)
		}
	}
	if globalDoc == "" || len(services) == 0 {
		t.Fatalf("expected a global document and services, got global=%v services=%v", globalDoc != "", services)
	}

	// Re-point the example's deployment paths at the sandbox (and the repo
	// catalog, so cross-references to packaged definitions keep working).
	var global map[string]any
	if err := yaml.Unmarshal([]byte(globalDoc), &global); err != nil {
		t.Fatalf("global document: %v", err)
	}
	global["paths"] = map[string]any{
		"catalog":  []any{filepath.Join(root, "catalog"), catalogExtra},
		"includes": []any{enabled},
		"mounts":   []any{mountsDir},
		"runtime":  filepath.Join(dir, "runtime"),
		"state":    filepath.Join(dir, "state"),
	}
	patched, err := yaml.Marshal(global)
	if err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(dir, "sermo.yml")
	if err := os.WriteFile(globalPath, patched, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(globalPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("docs/sermo-all.yml must validate cleanly, got: %v", issues)
	}
	for _, name := range services {
		resolved, errs := cfg.Resolve(name)
		if len(errs) != 0 {
			t.Errorf("service %s: resolve errors = %v", name, errs)
			continue
		}
		if len(resolved.Tree) == 0 {
			t.Errorf("service %s: empty resolved tree", name)
		}
	}
	for _, name := range mounts {
		resolved, errs := cfg.ResolveMount(name)
		if len(errs) != 0 {
			t.Errorf("mount %s: resolve errors = %v", name, errs)
			continue
		}
		if len(resolved.Tree) == 0 {
			t.Errorf("mount %s: empty resolved tree", name)
		}
	}
}
