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
	layout := materializeDocsExample(t, raw)
	cfg, err := Load(layout.globalPath, WithCatalogDirs(filepath.Join(root, "catalog"), layout.catalogExtra))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("docs/sermo-all.yml must validate cleanly, got: %v", issues)
	}
	assertDocsExampleServices(t, cfg, layout.services)
	assertDocsExampleWatches(t, cfg, layout.watches, layout.storageWatches)
}

type docsExampleLayout struct {
	catalogExtra   string
	globalPath     string
	services       []string
	watches        []string
	storageWatches []string
}

func materializeDocsExample(t *testing.T, raw []byte) docsExampleLayout {
	t.Helper()
	dir := t.TempDir()
	layout := docsExampleLayout{
		catalogExtra: filepath.Join(dir, "catalog-extra"),
	}
	servicesDir := filepath.Join(dir, "services")
	watchDirs := map[string]string{
		"watches":  filepath.Join(dir, "watches"),
		"networks": filepath.Join(dir, "networks"),
		"storages": filepath.Join(dir, "storages"),
		"mounts":   filepath.Join(dir, "mounts"),
	}
	createDocsExampleDirs(t, layout.catalogExtra, servicesDir, watchDirs)
	globalDoc := writeDocsExampleDocuments(t, raw, layout.catalogExtra, servicesDir, watchDirs, &layout)
	layout.globalPath = writeDocsExampleGlobal(t, dir, globalDoc, servicesDir, watchDirs)
	return layout
}

func createDocsExampleDirs(t *testing.T, catalogExtra, servicesDir string, watchDirs map[string]string) {
	t.Helper()
	for _, dir := range append([]string{servicesDir, catalogExtra}, mapValues(watchDirs)...) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, sub := range []string{"services", "apps", "libs", "patterns"} {
		if err := os.MkdirAll(filepath.Join(catalogExtra, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func writeDocsExampleDocuments(t *testing.T, raw []byte, catalogExtra, servicesDir string, watchDirs map[string]string, layout *docsExampleLayout) string {
	t.Helper()
	locMarker := regexp.MustCompile(`(?m)^# location:[[:space:]]*(\S+)`)
	var globalDoc string
	for index, doc := range strings.Split(string(raw), "\n---\n") {
		var body map[string]any
		if err := yaml.Unmarshal([]byte(doc), &body); err != nil {
			t.Fatalf("document %d does not parse: %v", index+1, err)
		}
		name, _ := body["name"].(string)
		location := locMarker.FindStringSubmatch(doc)
		if location == nil {
			if globalDoc != "" {
				t.Fatalf("document %d: second location-less (global) document", index+1)
			}
			globalDoc = doc
			continue
		}
		writeDocsExampleDocument(t, doc, name, location[1], catalogExtra, servicesDir, watchDirs, layout, index+1)
	}
	if globalDoc == "" || len(layout.services) == 0 {
		t.Fatalf("expected a global document and services, got global=%v services=%v", globalDoc != "", layout.services)
	}
	return globalDoc
}

func writeDocsExampleDocument(t *testing.T, doc, name, location, catalogExtra, servicesDir string, watchDirs map[string]string, layout *docsExampleLayout, index int) {
	t.Helper()
	var path string
	switch location {
	case "catalog/services", "catalog/apps", "catalog/libs", "catalog/patterns":
		path = filepath.Join(catalogExtra, strings.TrimPrefix(location, "catalog/"), name+".yml")
	case "services":
		layout.services = append(layout.services, name)
		path = filepath.Join(servicesDir, name+".yml")
	case "watches", "networks", "storages", "mounts":
		layout.watches = append(layout.watches, name)
		if location == "storages" || location == "mounts" {
			layout.storageWatches = append(layout.storageWatches, name)
		}
		path = filepath.Join(watchDirs[location], name+".yml")
	default:
		t.Fatalf("document %d: unknown location marker %q", index, location)
	}
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDocsExampleGlobal(t *testing.T, dir, globalDoc, servicesDir string, watchDirs map[string]string) string {
	t.Helper()
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
	return globalPath
}

func assertDocsExampleServices(t *testing.T, cfg *Config, services []string) {
	t.Helper()
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
}

func assertDocsExampleWatches(t *testing.T, cfg *Config, watches, storageWatches []string) {
	t.Helper()
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
