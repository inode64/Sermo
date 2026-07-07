package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// TestDocsSermoAllValidates loads docs/sermo-all.yml — the complete annotated
// configuration example — through the real loader and validator, so the
// reference file cannot drift from the schema. Each `---` document is placed
// where it would live in a deployment: the global part as sermo.yml, catalog
// kinds (service/app/lib/patterns) in a catalog dir, services in a services dir,
// and host watches in classified watch dirs; the example's absolute paths are
// rewritten to the test sandbox.
func TestDocsSermoAllValidates(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "docs", "sermo-all.yml"))
	if err != nil {
		t.Skipf("docs/sermo-all.yml not found: %v", err)
	}

	dir := t.TempDir()
	catalogExtra := filepath.Join(dir, "catalog-extra")
	servicesDir := filepath.Join(dir, "services")
	watchDirs := map[string]string{
		"watches":  filepath.Join(dir, "watches"),
		"networks": filepath.Join(dir, "networks"),
		"storages": filepath.Join(dir, "storages"),
		"mounts":   filepath.Join(dir, "mounts"),
	}
	for _, d := range append([]string{servicesDir, catalogExtra}, mapValues(watchDirs)...) {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Catalog documents are classified by subdirectory, mirroring the packaged
	// layout (catalog/{services,apps,libs,patterns}).
	for _, sub := range []string{"services", "apps", "libs", "patterns"} {
		if err := os.MkdirAll(filepath.Join(catalogExtra, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Each bundled document carries no `kind:` (it is derived from location, like
	// real config); a `# location: <dir>` marker says where it would live so the
	// test can lay it out on disk. The global document has no marker.
	locMarker := regexp.MustCompile(`(?m)^# location:[[:space:]]*(\S+)`)
	var globalDoc string
	var services []string
	var watches []string
	var storageWatches []string
	for i, doc := range strings.Split(string(raw), "\n---\n") {
		var body map[string]any
		if err := yaml.Unmarshal([]byte(doc), &body); err != nil {
			t.Fatalf("document %d does not parse: %v", i+1, err)
		}
		name, _ := body["name"].(string)
		m := locMarker.FindStringSubmatch(doc)
		if m == nil {
			if globalDoc != "" {
				t.Fatalf("document %d: second location-less (global) document", i+1)
			}
			globalDoc = doc
			continue
		}
		switch loc := m[1]; loc {
		case "catalog/services", "catalog/apps", "catalog/libs", "catalog/patterns":
			sub := strings.TrimPrefix(loc, "catalog/")
			if err := os.WriteFile(filepath.Join(catalogExtra, sub, name+".yml"), []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
		case "services":
			services = append(services, name)
			if err := os.WriteFile(filepath.Join(servicesDir, name+".yml"), []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
		case "watches", "networks", "storages", "mounts":
			watches = append(watches, name)
			if loc == "storages" || loc == "mounts" {
				storageWatches = append(storageWatches, name)
			}
			if err := os.WriteFile(filepath.Join(watchDirs[loc], name+".yml"), []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("document %d: unknown location marker %q", i+1, loc)
		}
	}
	if globalDoc == "" || len(services) == 0 {
		t.Fatalf("expected a global document and services, got global=%v services=%v", globalDoc != "", services)
	}

	// Re-point the example's deployment paths at the sandbox. Catalog documents
	// are loaded through the internal test override below, not YAML.
	var global map[string]any
	if err := yaml.Unmarshal([]byte(globalDoc), &global); err != nil {
		t.Fatalf("global document: %v", err)
	}
	global["paths"] = map[string]any{
		"services": []any{servicesDir},
		"watches":  stringsToAny(mapValues(watchDirs)),
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

	cfg, err := Load(globalPath, WithCatalogDirs(filepath.Join(root, "catalog"), catalogExtra))
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
	resolvedWatches, errs := cfg.ResolveWatches()
	if len(errs) != 0 {
		t.Fatalf("resolve watches: %v", errs)
	}
	for _, name := range watches {
		if _, ok := resolvedWatches[name]; !ok {
			t.Errorf("watch %s: missing from resolved watches", name)
		}
	}
	for _, name := range storageWatches {
		resolved, errs := cfg.ResolveStorage(name)
		if len(errs) != 0 {
			t.Errorf("storage watch %s: resolve errors = %v", name, errs)
			continue
		}
		if len(resolved.Tree) == 0 {
			t.Errorf("storage watch %s: empty resolved tree", name)
		}
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, value := range m {
		out = append(out, value)
	}
	return out
}

func stringsToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
