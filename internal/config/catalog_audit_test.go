package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"sermo/internal/cfgval"
)

// These audits load the real repo artifacts — the packaged catalog, the shipped
// sermo.yml and the examples — and require them to resolve and validate
// cleanly, so a catalog definition that no current service exercises (the way
// kafka's nested variables and rabbitmq's incomplete kill_only_if once shipped
// broken) cannot regress unnoticed.

// repoRoot returns the repository root, skipping the test when the catalog is
// not present (e.g. a vendored build of just this package).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "catalog")); err != nil {
		t.Skipf("catalog dir not found: %v", err)
	}
	return root
}

// TestRealCatalogAllDaemonsValidate enables every instantiable catalog daemon
// as a service and validates the whole set. Version templates (%v/%n/%i) cannot
// be materialized off-host, so only the concrete daemon names are exercised.
func TestRealCatalogAllDaemonsValidate(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")

	writeGlobal := func(dir, enabled string) string {
		global := filepath.Join(dir, "sermo.yml")
		body := "engine: { backend: systemd }\n" +
			"paths:\n  catalog: [" + catalogDir + "]\n  includes: [" + enabled + "]\n  runtime: /run/sermo\n" +
			"defaults:\n  policy: { cooldown: 5m }\n"
		if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return global
	}

	// First load with no services, just to enumerate the daemon registry.
	probeDir := t.TempDir()
	emptyEnabled := filepath.Join(probeDir, "enabled")
	if err := os.MkdirAll(emptyEnabled, 0o755); err != nil {
		t.Fatal(err)
	}
	probe, err := Load(writeGlobal(probeDir, emptyEnabled))
	if err != nil {
		t.Fatalf("Load (probe): %v", err)
	}

	dir := t.TempDir()
	enabled := filepath.Join(dir, "enabled")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, name := range probe.DaemonNames {
		if strings.Contains(name, "%") {
			continue
		}
		svc := "kind: service\nname: " + name + "-audit\nuses: " + name + "\n"
		if err := os.WriteFile(filepath.Join(enabled, name+".yml"), []byte(svc), 0o644); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count == 0 {
		t.Fatal("no instantiable catalog daemons found")
	}

	cfg, err := Load(writeGlobal(dir, enabled))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, issue := range Validate(cfg) {
		t.Errorf("catalog daemon fails validation: %s", issue)
	}
}

// TestShippedGlobalConfigValidates points the shipped configs/sermo.yml at the
// repo catalog and validates it with its bundled apps services.
func TestShippedGlobalConfigValidates(t *testing.T) {
	root := repoRoot(t)

	src, err := os.ReadFile(filepath.Join(root, "configs", "sermo.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(string(src), "/usr/share/sermo/catalog", filepath.Join(root, "catalog"))
	body = strings.ReplaceAll(body, "    - /etc/sermo/catalog-available\n", "")
	if body == string(src) {
		t.Fatal("configs/sermo.yml no longer lists the packaged catalog paths; update this rewrite")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sermo.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// The shipped config enables no services out of the box; when bundled
	// include dirs reappear, copy them so their services validate too. `apps`
	// is a legacy include alias for concrete service files.
	for _, include := range []string{"services", "apps"} {
		if bundled := filepath.Join(root, "configs", include); dirExists(bundled) {
			copyYAMLDir(t, bundled, filepath.Join(dir, include))
		}
	}

	cfg, err := Load(filepath.Join(dir, "sermo.yml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, issue := range Validate(cfg) {
		t.Errorf("shipped sermo.yml fails validation: %s", issue)
	}
}

func TestShippedServiceConfigsLiveUnderServices(t *testing.T) {
	root := repoRoot(t)
	servicesDir := filepath.Join(root, "configs", "services")
	if !dirExists(servicesDir) {
		t.Fatalf("configs/services is missing")
	}
	services, err := yamlFiles(servicesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) == 0 {
		t.Fatalf("configs/services has no service examples")
	}

	appsDir := filepath.Join(root, "configs", "apps")
	if !dirExists(appsDir) {
		return
	}
	apps, err := yamlFiles(appsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) > 0 {
		t.Fatalf("configs/apps is a legacy alias; move service YAML examples to configs/services: %s", strings.Join(apps, ", "))
	}
}

func TestCatalogAppsDoNotDeclareServiceProcessSelectors(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "catalog", "apps")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !isYAML(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		var found []string
		collectForbiddenKeys(doc, "", map[string]struct{}{"pidfile": {}, "processes": {}}, &found)
		if len(found) > 0 {
			t.Errorf("%s declares service process selector keys in catalog/apps: %s", path, strings.Join(found, ", "))
		}
	}
}

func TestCatalogDaemonsUseCanonicalServiceNames(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  includes: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := map[string][]string{
		"avahi":    {"avahi", "avahi-daemon"},
		"cups":     {"cupsd"},
		"dbus":     {"dbus", "dbus-daemon"},
		"fail2ban": {"fail2ban", "fail2ban-server"},
		"keydb":    {"keydb", "keydb-server"},
	}
	for name, openrcCandidates := range want {
		resolved, errs := cfg.ResolveCatalog(CategoryService, name)
		if len(errs) > 0 {
			t.Fatalf("ResolveCatalog(%s): %v", name, errs)
		}
		if resolved.Name != name {
			t.Fatalf("ResolveCatalog(%s) resolved name = %q", name, resolved.Name)
		}
		candidates, trust := ServiceCandidates(resolved.Tree, "openrc", name)
		if trust {
			t.Fatalf("ServiceCandidates(%s) trust = true, want explicit aliases", name)
		}
		if strings.Join(candidates, ",") != strings.Join(openrcCandidates, ",") {
			t.Fatalf("ServiceCandidates(%s) = %v, want %v", name, candidates, openrcCandidates)
		}
	}

	legacy := map[string]string{
		"avahi-daemon":    "avahi",
		"cups-config":     "cups",
		"dbus-daemon":     "dbus",
		"fail2ban-server": "fail2ban",
		"keydb-server":    "keydb",
	}
	listed := map[string]struct{}{}
	for _, name := range cfg.DaemonsInCategory(CategoryService) {
		listed[name] = struct{}{}
	}
	for oldName, canonical := range legacy {
		if _, ok := listed[oldName]; ok {
			t.Fatalf("legacy daemon alias %q should not be listed as a catalog service", oldName)
		}
		doc, ok := cfg.Daemons[oldName]
		if !ok {
			t.Fatalf("legacy daemon alias %q does not resolve", oldName)
		}
		if doc.Name != canonical {
			t.Fatalf("legacy daemon alias %q resolves to %q, want %q", oldName, doc.Name, canonical)
		}
	}
}

func TestCatalogAppsUseCanonicalNames(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  includes: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	legacy := map[string]string{
		"avahi-daemon":    "avahi",
		"dbus-daemon":     "dbus",
		"fail2ban-server": "fail2ban",
		"keydb-server":    "keydb",
	}
	listed := map[string]struct{}{}
	for _, name := range cfg.DaemonsInCategory(CategoryApp) {
		listed[name] = struct{}{}
	}
	for oldName, canonical := range legacy {
		if _, ok := listed[oldName]; ok {
			t.Fatalf("legacy app alias %q should not be listed as a catalog app", oldName)
		}
		doc, ok := cfg.Apps[oldName]
		if !ok {
			t.Fatalf("legacy app alias %q does not resolve", oldName)
		}
		if doc.Name != canonical {
			t.Fatalf("legacy app alias %q resolves to %q, want %q", oldName, doc.Name, canonical)
		}
		if _, ok := listed[canonical]; !ok {
			t.Fatalf("canonical app %q should be listed", canonical)
		}
	}
}

func TestCatalogCupsUsesSingleCupsdApp(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  includes: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	resolved, errs := cfg.ResolveCatalog(CategoryService, "cups")
	if len(errs) != 0 {
		t.Fatalf("ResolveCatalog(cups): %v", errs)
	}
	if _, errs := cfg.ResolveCatalog(CategoryService, "cups-config"); len(errs) != 0 {
		t.Fatalf("ResolveCatalog(cups-config alias): %v", errs)
	}
	preflight := resolved.Tree["preflight"].(map[string]any)
	config := preflight["config"].(map[string]any)
	command := config["command"].([]any)
	if got := command[0]; got != "/usr/bin/cupsd" {
		t.Fatalf("cups config command = %v, want cupsd app binary", command)
	}
	tool := preflight["cupsd-cups-config"].(map[string]any)
	if got := tool["path"]; got != "/usr/bin/cups-config" {
		t.Fatalf("cupsd cups-config path = %v, want /usr/bin/cups-config", got)
	}
	health := preflight["cupsd-health"].(map[string]any)
	healthCommand := health["command"].([]any)
	if len(healthCommand) != 2 || healthCommand[0] != "/usr/bin/cups-config" || healthCommand[1] != "-h" {
		t.Fatalf("cupsd health command = %v, want /usr/bin/cups-config -h", healthCommand)
	}
	version := preflight["cupsd-version"].(map[string]any)
	versionCommand := version["command"].([]any)
	if len(versionCommand) != 2 || versionCommand[0] != "/usr/bin/cups-config" || versionCommand[1] != "--version" {
		t.Fatalf("cupsd version command = %v, want /usr/bin/cups-config --version", versionCommand)
	}
}

func TestCatalogServicesReuseLinkedAppBinaries(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  includes: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, name := range cfg.DaemonNames {
		doc := cfg.Daemons[name]
		serviceBinary := catalogBinary(doc)
		if serviceBinary == "" {
			continue
		}
		for _, appName := range cfgval.StringList(doc.Body["apps"]) {
			appDoc, ok := cfg.Apps[appName]
			if !ok {
				continue
			}
			if serviceBinary != catalogBinary(appDoc) {
				continue
			}
			t.Errorf("%s defines variables.binary %q already owned by app %s; use ${%s_binary} instead", name, serviceBinary, appName, appVariablePrefix(appName))
			if hasVersionProbe(doc.Body) {
				t.Errorf("%s defines a service-level version probe already owned by app %s", name, appName)
			}
		}
	}
}

func catalogBinary(doc *Document) string {
	if doc == nil {
		return ""
	}
	vars, _ := doc.Body["variables"].(map[string]any)
	return cfgval.String(vars["binary"])
}

func hasVersionProbe(body map[string]any) bool {
	if preflight, _ := body["preflight"].(map[string]any); preflight != nil {
		if _, ok := preflight["version"]; ok {
			return true
		}
	}
	if commands, _ := body["commands"].(map[string]any); commands != nil {
		if _, ok := commands["version"]; ok {
			return true
		}
	}
	return false
}

func yamlFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if !entry.IsDir() && isYAML(entry.Name()) {
			out = append(out, entry.Name())
		}
	}
	return out, nil
}

func collectForbiddenKeys(node any, keyPath string, forbidden map[string]struct{}, found *[]string) {
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			next := key
			if keyPath != "" {
				next = keyPath + "." + key
			}
			if _, ok := forbidden[key]; ok {
				*found = append(*found, next)
			}
			collectForbiddenKeys(child, next, forbidden, found)
		}
	case []any:
		for _, child := range v {
			collectForbiddenKeys(child, keyPath, forbidden, found)
		}
	}
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// copyYAMLDir copies the top-level *.yml files of src into dst.
func copyYAMLDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
