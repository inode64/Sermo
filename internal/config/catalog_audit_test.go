package config

import (
	"os"
	"path/filepath"
	"slices"
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

	writeGlobal := func(dir, enabled, backend string) string {
		global := filepath.Join(dir, "sermo.yml")
		body := "engine: { backend: " + backend + " }\n" +
			"paths:\n  catalog: [" + catalogDir + "]\n  services: [" + enabled + "]\n  runtime: /run/sermo\n" +
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
	probe, err := Load(writeGlobal(probeDir, emptyEnabled, "systemd"))
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

	for _, backend := range []string{"systemd", "openrc"} {
		t.Run(backend, func(t *testing.T) {
			cfg, err := Load(writeGlobal(dir, enabled, backend))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			for _, issue := range Validate(cfg) {
				t.Errorf("catalog daemon fails validation: %s", issue)
			}
		})
	}
}

// TestShippedGlobalConfigValidates points the shipped examples/sermo.yml at the
// repo catalog and validates it with its bundled apps services.
func TestShippedGlobalConfigValidates(t *testing.T) {
	root := repoRoot(t)

	src, err := os.ReadFile(filepath.Join(root, "examples", "sermo.yml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	body := strings.ReplaceAll(string(src), "/usr/share/sermo/catalog", filepath.Join(root, "catalog"))
	body = strings.ReplaceAll(body, "    - /etc/sermo/catalog-available\n", "")
	if body == string(src) {
		t.Fatal("examples/sermo.yml no longer lists the packaged catalog paths; update this rewrite")
	}
	for _, name := range []string{"services", "apps", "notifiers", "storages", "networks", "watches", "mounts"} {
		body = strings.ReplaceAll(body, "/etc/sermo/"+name, filepath.Join(dir, name))
	}

	if err := os.WriteFile(filepath.Join(dir, "sermo.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// The shipped config enables no services out of the box; when bundled target
	// dirs reappear, copy them so their entries validate too.
	for _, include := range []string{"services", "apps", "notifiers", "storages", "networks", "watches", "mounts"} {
		if bundled := filepath.Join(root, "examples", include); dirExists(bundled) {
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
	servicesDir := filepath.Join(root, "examples", "services")
	if !dirExists(servicesDir) {
		t.Fatalf("examples/services is missing")
	}
	services, err := yamlFiles(servicesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) == 0 {
		t.Fatalf("examples/services has no service examples")
	}

	assertExampleDocsHaveKind(t, filepath.Join(root, "examples", "apps"), kindApp)
}

func TestShippedServiceConfigExamplesValidate(t *testing.T) {
	root := repoRoot(t)
	servicesDir := filepath.Join(root, "examples", "services")
	if !dirExists(servicesDir) {
		t.Fatalf("examples/services is missing")
	}

	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + filepath.Join(root, "catalog") + "]\n  services: [" + servicesDir + "]\n  runtime: /run/sermo\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Services) == 0 {
		t.Fatalf("examples/services has no loadable service examples")
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("examples/services examples must validate cleanly, got: %v", issues)
	}

	tests := []struct {
		service   string
		check     string
		preflight string
		binaries  []string
	}{
		{
			service:   "mariadb-backup-guard",
			check:     "mariadb-backup",
			preflight: "mariadb-backup-binary",
			binaries:  []string{"/usr/bin/mariadb-backup", "/usr/bin/mariadbbackup"},
		},
		{
			service:   "mysql-wal-g-backup-guard",
			check:     "wal-g-mysql",
			preflight: "wal-g-mysql-binary",
			binaries:  []string{"/usr/bin/wal-g-mysql", "/usr/local/bin/wal-g-mysql", "/usr/bin/wal-g", "/usr/local/bin/wal-g"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			resolved, errs := cfg.Resolve(tt.service)
			if len(errs) != 0 {
				t.Fatalf("Resolve(%s): %v", tt.service, errs)
			}
			exe := cfgval.String(valueAt(t, resolved.Tree, "checks", tt.check, "exe"))
			if !slices.Contains(tt.binaries, exe) {
				t.Fatalf("%s %s exe = %q, want one of %v", tt.service, tt.check, exe, tt.binaries)
			}
			preflight := nested(t, resolved.Tree, "preflight")
			entry, ok := preflight[tt.preflight].(map[string]any)
			if !ok {
				t.Fatalf("%s lacks app preflight %q: %v", tt.service, tt.preflight, preflight)
			}
			if got := cfgval.Bool(entry["optional"]); got {
				t.Fatalf("%s preflight %q optional = %v, want false", tt.service, tt.preflight, got)
			}
		})
	}
}

func TestGentooCatalogPidfileOverrides(t *testing.T) {
	old := detectedOS
	detectedOS = "gentoo"
	defer func() { detectedOS = old }()

	root := repoRoot(t)
	dir := t.TempDir()
	enabled := filepath.Join(dir, "enabled")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(dir, "sermo.yml")
	body := "engine: { backend: openrc }\n" +
		"paths:\n  catalog: [" + filepath.Join(root, "catalog") + "]\n  services: [" + enabled + "]\n  runtime: /run/sermo\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"clamd", "mariadb"} {
		svc := "kind: service\nname: " + name + "\nuses: " + name + "\n"
		if err := os.WriteFile(filepath.Join(enabled, name+".yml"), []byte(svc), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tests := []struct {
		name string
		want []string
	}{
		{name: "clamd", want: []string{"/run/clamd.pid"}},
		{name: "mariadb", want: []string{"/run/mysqld/mariadb.pid", "/run/mysqld/mysqld.pid"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolved, errs := cfg.Resolve(tc.name)
			if len(errs) != 0 {
				t.Fatalf("Resolve() errors = %v", errs)
			}
			proc := nested(t, resolved.Tree, "processes", "pidfile")
			if got := cfgval.StringList(proc["path"]); !slices.Equal(got, tc.want) {
				t.Fatalf("process pidfile = %q, want %q", got, tc.want)
			}
			check := nested(t, resolved.Tree, "checks", "pidfile")
			if got := cfgval.StringList(check["path"]); !slices.Equal(got, tc.want) {
				t.Fatalf("check pidfile = %q, want %q", got, tc.want)
			}
		})
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

func TestCatalogUnifiUsesMongodAppBinary(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "catalog", "services", "unifi.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse unifi catalog: %v", err)
	}
	if apps := strings.Join(cfgval.StringList(doc["apps"]), ","); apps != "java,mongod" {
		t.Fatalf("unifi apps = %q, want java,mongod", apps)
	}
	processes, ok := doc["processes"].(map[string]any)
	if !ok {
		t.Fatalf("unifi processes missing or invalid: %v", doc["processes"])
	}
	mongo, ok := processes["mongo"].(map[string]any)
	if !ok {
		t.Fatalf("unifi mongo process selector missing or invalid: %v", processes["mongo"])
	}
	if got := cfgval.String(mongo["exe"]); got != "${mongod_binary}" {
		t.Fatalf("unifi mongo exe = %q, want app variable ${mongod_binary}", got)
	}

	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + filepath.Join(root, "catalog") + "]\n  services: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	resolved, errs := cfg.ResolveCatalog(CategoryService, "unifi")
	if len(errs) > 0 {
		t.Fatalf("ResolveCatalog(unifi): %v", errs)
	}
	resolvedProcesses, ok := resolved.Tree["processes"].(map[string]any)
	if !ok {
		t.Fatalf("resolved unifi processes missing or invalid: %v", resolved.Tree["processes"])
	}
	resolvedMongo, ok := resolvedProcesses["mongo"].(map[string]any)
	if !ok {
		t.Fatalf("resolved unifi mongo process selector missing or invalid: %v", resolvedProcesses["mongo"])
	}
	if got := cfgval.String(resolvedMongo["exe"]); got != "/usr/bin/mongod" {
		t.Fatalf("resolved unifi mongo exe = %q, want /usr/bin/mongod", got)
	}
	preflight, ok := resolved.Tree["preflight"].(map[string]any)
	if !ok {
		t.Fatalf("resolved unifi preflight missing or invalid: %v", resolved.Tree["preflight"])
	}
	if _, ok := preflight["mongod-binary"]; !ok {
		t.Fatalf("resolved unifi preflight lacks mongod-binary: %v", preflight)
	}
}

func TestCatalogDaemonsUseCanonicalServiceNames(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  services: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := map[string][]string{
		"automount":    {"autofs", "automount"},
		"avahi":        {"avahi", "avahi-daemon"},
		"cups":         {"cupsd"},
		"dbus":         {"dbus", "dbus-daemon"},
		"fail2ban":     {"fail2ban", "fail2ban-server"},
		"in.tftpd":     {"in.tftpd", "in-tftpd"},
		"keydb":        {"keydb", "keydb-server"},
		"lm_sensors":   {"lm_sensors", "lm-sensors"},
		"qemu-ga":      {"qemu-guest-agent", "qemu-ga"},
		"rpc-mountd":   {"rpc-mountd", "nfs-mountd"},
		"rsync":        {"rsyncd", "rsync"},
		"spamassassin": {"spamd", "spamassassin"},
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
			t.Fatalf("ServiceCandidates(%s) trust = true, want explicit candidates", name)
		}
		if strings.Join(candidates, ",") != strings.Join(openrcCandidates, ",") {
			t.Fatalf("ServiceCandidates(%s) = %v, want %v", name, candidates, openrcCandidates)
		}
	}

	resolved, errs := cfg.ResolveCatalog(CategoryService, "rpc-mountd")
	if len(errs) > 0 {
		t.Fatalf("ResolveCatalog(rpc-mountd): %v", errs)
	}
	systemdCandidates, trust := ServiceCandidates(resolved.Tree, "systemd", "rpc-mountd")
	if trust {
		t.Fatalf("ServiceCandidates(rpc-mountd systemd) trust = true, want explicit candidates")
	}
	wantSystemdCandidates := []string{"nfs-mountd", "rpc-mountd"}
	if strings.Join(systemdCandidates, ",") != strings.Join(wantSystemdCandidates, ",") {
		t.Fatalf("ServiceCandidates(rpc-mountd systemd) = %v, want %v", systemdCandidates, wantSystemdCandidates)
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
	body := "paths:\n  catalog: [" + catalogDir + "]\n  services: []\n" +
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
		"lm-sensors":      "lm_sensors",
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

func TestCatalogAppsDeclareVersionSource(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  services: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	noLocalVersion := map[string]string{
		"libvirt-dbus": "upstream documents no version option for libvirt-dbus",
		"udisks2":      "upstream documents no version option for udisksd or udisksctl",
	}
	for _, name := range cfg.DaemonsInCategory(CategoryApp) {
		doc := cfg.Apps[name]
		if hasVersionProbe(doc.Body) {
			continue
		}
		if source := cfgval.String(doc.Body["version_from"]); source != "" {
			if !catalogAppProvidesVersion(cfg, source, map[string]bool{name: true}) {
				t.Errorf("%s version_from %q does not resolve to an app with a version probe", name, source)
			}
			continue
		}
		if reason := noLocalVersion[name]; reason == "" {
			t.Errorf("%s has no version probe, version_from, or documented exception", name)
		}
	}
}

func TestCatalogAppsDeclareHealthOrVersionSource(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  services: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	noSafeHealth := map[string]string{
		"nfsdcld": "upstream documents no help/version option; version comes from rpc-mountd",
		"rpcbind": "upstream documents version output but no separate help/health option; version comes from rpc-mountd",
	}
	for _, name := range cfg.DaemonsInCategory(CategoryApp) {
		doc := cfg.Apps[name]
		if hasHealthProbe(doc.Body) || hasVersionProbe(doc.Body) {
			continue
		}
		if source := cfgval.String(doc.Body["version_from"]); source != "" {
			if reason := noSafeHealth[name]; reason == "" {
				t.Errorf("%s has version_from %q but no local health probe", name, source)
			}
			continue
		}
		t.Errorf("%s has no health probe, version probe, or version_from", name)
	}
}

func TestCatalogOptionalAppVersionsRequireHealth(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  services: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for _, name := range cfg.DaemonsInCategory(CategoryApp) {
		doc := cfg.Apps[name]
		if !versionProbeOptional(doc.Body) {
			continue
		}
		if !hasHealthProbe(doc.Body) {
			t.Errorf("%s has optional version but no health probe", name)
		}
	}
}

func TestCatalogAppsUseSharedVersionProviders(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  services: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	sharedVersions := map[string]string{
		"pmcd":          "pcp",
		"pmie":          "pcp",
		"pmie_farm":     "pcp",
		"pmlogger":      "pcp",
		"pmlogger_farm": "pcp",
		"rpcbind":       "rpc-mountd",
	}
	for app, provider := range sharedVersions {
		doc, ok := cfg.Apps[app]
		if !ok {
			t.Fatalf("shared-version app %q missing", app)
		}
		if got := cfgval.String(doc.Body["version_from"]); got != provider {
			t.Fatalf("%s version_from = %q, want %q", app, got, provider)
		}
		if hasVersionProbe(doc.Body) {
			t.Fatalf("%s duplicates provider %s with a local version probe", app, provider)
		}
		providerDoc, ok := cfg.Apps[provider]
		if !ok {
			t.Fatalf("version provider %q for %s missing", provider, app)
		}
		if !hasVersionProbe(providerDoc.Body) {
			t.Fatalf("version provider %q for %s has no version probe", provider, app)
		}
	}
}

func TestCatalogCupsUsesSingleCupsdApp(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  services: []\n" +
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
	if len(healthCommand) != 2 || healthCommand[0] != "/usr/bin/cups-config" || healthCommand[1] != "--help" {
		t.Fatalf("cupsd health command = %v, want /usr/bin/cups-config --help", healthCommand)
	}
	version := preflight["cupsd-version"].(map[string]any)
	versionCommand := version["command"].([]any)
	if len(versionCommand) != 2 || versionCommand[0] != "/usr/bin/cups-config" || versionCommand[1] != "--version" {
		t.Fatalf("cupsd version command = %v, want /usr/bin/cups-config --version", versionCommand)
	}
}

func TestCatalogConfigPreflightsUseResolvedAppTools(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  services: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	tests := []struct {
		service      string
		appToolCheck string
		toolArgIndex int
		wantTool     []string
		wantContains []string
	}{
		{
			service:      "docker",
			appToolCheck: "docker-daemon",
			toolArgIndex: 3,
			wantTool:     []string{"/usr/bin/dockerd", "/usr/sbin/dockerd"},
			wantContains: []string{"--validate", "--config-file"},
		},
		{
			service:      "firewalld",
			appToolCheck: "firewalld-binary_offline",
			toolArgIndex: 0,
			wantTool:     []string{"/usr/bin/firewall-offline-cmd"},
			wantContains: []string{"--check-config", "--system-config", "/etc/firewalld"},
		},
		{
			service:      "fetchmail",
			appToolCheck: "fetchmail-binary",
			toolArgIndex: 3,
			wantTool:     []string{"/usr/bin/fetchmail", "/usr/sbin/fetchmail"},
			wantContains: []string{"--configdump", "-f"},
		},
		{
			service:      "nmbd",
			appToolCheck: "nmbd-testparm",
			toolArgIndex: 0,
			wantTool:     []string{"/usr/bin/testparm", "/usr/sbin/testparm"},
			wantContains: []string{"-s"},
		},
		{
			service:      "slapd",
			appToolCheck: "slapd-slaptest",
			toolArgIndex: 3,
			wantTool:     []string{"/usr/sbin/slaptest", "/usr/bin/slaptest", "/usr/bin/openldap/slaptest"},
			wantContains: []string{"-Q", "-u"},
		},
		{
			service:      "nebula",
			appToolCheck: "nebula-binary",
			toolArgIndex: 0,
			wantTool:     []string{"/usr/bin/nebula"},
			wantContains: []string{"-test", "-config"},
		},
		{
			service:      "loki",
			appToolCheck: "loki-binary",
			toolArgIndex: 0,
			wantTool:     []string{"/usr/bin/loki"},
			wantContains: []string{"-verify-config", "-config.file"},
		},
		{
			service:      "influxdb",
			appToolCheck: "influxdb-binary",
			toolArgIndex: 0,
			wantTool:     []string{"/usr/bin/influxd"},
			wantContains: []string{"config", "validate", "--config"},
		},
		{
			service:      "cloudflared",
			appToolCheck: "cloudflared-binary",
			toolArgIndex: 3,
			wantTool:     []string{"/usr/bin/cloudflared"},
			wantContains: []string{"tunnel", "validate"},
		},
		{
			service:      "mysql",
			appToolCheck: "mysql-binary",
			toolArgIndex: 0,
			wantTool:     []string{"/usr/sbin/mysqld", "/usr/bin/mysqld"},
			wantContains: []string{"--defaults-file=", "--validate-config"},
		},
		{
			service:      "mariadb",
			appToolCheck: "mariadb-binary",
			toolArgIndex: 0,
			wantTool:     []string{"/usr/sbin/mariadbd", "/usr/bin/mariadbd"},
			wantContains: []string{"--defaults-file=", "--help", "--verbose"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.service, func(t *testing.T) {
			resolved, errs := cfg.ResolveCatalog(CategoryService, tc.service)
			if len(errs) != 0 {
				t.Fatalf("ResolveCatalog(%s): %v", tc.service, errs)
			}
			preflight := nested(t, resolved.Tree, "preflight")
			tool := cfgval.String(nested(t, preflight, tc.appToolCheck)["path"])
			if !slices.Contains(tc.wantTool, tool) {
				t.Fatalf("%s app tool path = %q, want one of %v", tc.service, tool, tc.wantTool)
			}
			command := nested(t, preflight, "config")["command"].([]any)
			if tc.toolArgIndex >= len(command) {
				t.Fatalf("%s config command = %v, missing tool arg index %d", tc.service, command, tc.toolArgIndex)
			}
			if got := cfgval.String(command[tc.toolArgIndex]); got != tool {
				t.Fatalf("%s config command tool = %q, want resolved app tool %q in %v", tc.service, got, tool, command)
			}
			joined := strings.Join(cfgval.StringList(command), " ")
			for _, want := range tc.wantContains {
				if !strings.Contains(joined, want) {
					t.Fatalf("%s config command = %v, want token %q", tc.service, command, want)
				}
			}
		})
	}
}

func TestCatalogNamedDNSCheckIsHostOverrideFriendly(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "catalog", "services", "named.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := yaml.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}

	vars := nested(t, body, "variables")
	for _, key := range []string{"host", "port", "query"} {
		if cfgval.String(vars[key]) == "" {
			t.Fatalf("named variables must include %q so host-specific listeners can be overridden: %v", key, vars)
		}
	}
	check := nested(t, body, "checks", "port")
	if got := cfgval.String(check["host"]); got != "${host}" {
		t.Fatalf("named DNS check host = %q, want ${host}", got)
	}
	if got := cfgval.String(check["port"]); got != "${port}" {
		t.Fatalf("named DNS check port = %q, want ${port}", got)
	}
	if got := cfgval.String(check["query"]); got != "${query}" {
		t.Fatalf("named DNS check query = %q, want ${query}", got)
	}
}

func TestCatalogRAIDChecksAlertOnDegradedArrays(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{"mdadm", "mdmonitor"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, "catalog", "services", name+".yml")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := yaml.Unmarshal(data, &body); err != nil {
				t.Fatal(err)
			}
			degraded := nested(t, body, "checks", "raid", "degraded")
			if got := cfgval.String(degraded["op"]); got != ">" {
				t.Fatalf("%s raid degraded op = %q, want >", name, got)
			}
			if got := cfgval.String(degraded["value"]); got != "0" {
				t.Fatalf("%s raid degraded value = %q, want 0", name, got)
			}
		})
	}
}

func TestRequestedHostProfilesExist(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  services: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	tests := []struct {
		name        string
		app         string
		binaryVar   string
		wantProcess bool
	}{
		{name: "containerd", app: "containerd", binaryVar: "${containerd_binary}", wantProcess: true},
		{name: "libvirt-dbus", app: "libvirt-dbus", binaryVar: "${libvirt_dbus_binary}", wantProcess: true},
		{name: "nfsdcld", app: "nfsdcld", binaryVar: "${nfsdcld_binary}", wantProcess: true},
		{name: "lm_sensors", app: "lm_sensors", wantProcess: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, ok := cfg.Daemons[tc.name]
			if !ok {
				t.Fatalf("service catalog %q not found", tc.name)
			}
			if _, ok := cfg.Apps[tc.app]; !ok {
				t.Fatalf("app catalog %q not found", tc.app)
			}
			if !slices.Contains(cfgval.StringList(doc.Body["apps"]), tc.app) {
				t.Fatalf("%s apps = %v, want %s", tc.name, doc.Body["apps"], tc.app)
			}
			resolved, errs := cfg.ResolveCatalog(CategoryService, tc.name)
			if len(errs) > 0 {
				t.Fatalf("ResolveCatalog(%s): %v", tc.name, errs)
			}
			check := nested(t, resolved.Tree, "checks", "service")
			if got := cfgval.String(check["type"]); got != "service" {
				t.Fatalf("%s service check type = %q, want service", tc.name, got)
			}
			if got := cfgval.String(check["expect"]); got != "active" {
				t.Fatalf("%s service check expect = %q, want active", tc.name, got)
			}
			processes, ok := doc.Body["processes"].(map[string]any)
			if !tc.wantProcess {
				if !ok || len(processes) != 0 {
					t.Fatalf("%s processes = %v, want empty map for oneshot service", tc.name, doc.Body["processes"])
				}
				return
			}
			if !ok {
				t.Fatalf("%s missing process selector", tc.name)
			}
			main := nested(t, doc.Body, "processes", "main")
			if got := cfgval.String(main["exe"]); got != tc.binaryVar {
				t.Fatalf("%s process exe = %q, want %q", tc.name, got, tc.binaryVar)
			}
			if got := cfgval.String(main["user"]); got != "${user}" {
				t.Fatalf("%s process user = %q, want ${user}", tc.name, got)
			}
		})
	}
}

func TestCatalogPHPFPMVersionedConfigTestUsesConfigFile(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "catalog", "services", "php-fpm%v.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := yaml.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if got := cfgval.String(nested(t, body, "variables")["config"]); got != "/etc/php/fpm-php${version}/php-fpm.conf" {
		t.Fatalf("php-fpm config variable = %q", got)
	}
	config := nested(t, body, "preflight", "config")
	command, _ := config["command"].([]any)
	want := []any{"${binary}", "--test", "--fpm-config", "${config}", "--pid", "${pidfile}"}
	if len(command) != len(want) {
		t.Fatalf("php-fpm config command = %v, want %v", command, want)
	}
	for i := range want {
		if command[i] != want[i] {
			t.Fatalf("php-fpm config command = %v, want %v", command, want)
		}
	}
	rules := nested(t, body, "rules")
	if _, ok := rules["restart-if-tcp-failed"]; ok {
		t.Fatal("php-fpm must not remediate on the optional tcp check by default")
	}
}

func TestCatalogVarnishAdminChecksAreOptional(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "catalog", "services", "varnishd.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := yaml.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	checks := nested(t, body, "checks")
	for _, name := range []string{"port", "varnish"} {
		check, _ := checks[name].(map[string]any)
		if !cfgval.Bool(check["optional"]) {
			t.Fatalf("varnishd check %q optional = %v, want true", name, check["optional"])
		}
	}
}

func TestCatalogServicesUseAppVariablesForBinaryRefs(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  services: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	tests := []struct {
		name              string
		service           string
		path              []any
		wantRaw           string
		wantResolved      string
		preflight         string
		preflightOptional bool
	}{
		{
			name:         "rspamd config uses rspamadm from app",
			service:      "rspamd",
			path:         []any{"preflight", "config", "command", 0},
			wantRaw:      "${rspamd_rspamadm}",
			wantResolved: "/usr/bin/rspamadm",
			preflight:    "rspamd-rspamadm",
		},
		{
			name:         "smbd config uses testparm from app",
			service:      "smbd",
			path:         []any{"preflight", "config", "command", 0},
			wantRaw:      "${smbd_testparm}",
			wantResolved: "/usr/bin/testparm",
			preflight:    "smbd-testparm",
		},
		{
			name:         "dovecot config uses doveconf from app",
			service:      "dovecot",
			path:         []any{"preflight", "config", "command", 0},
			wantRaw:      "${dovecot_doveconf}",
			wantResolved: "/usr/bin/doveconf",
			preflight:    "dovecot-doveconf",
		},
		{
			name:         "rpcbind process uses app binary",
			service:      "rpcbind",
			path:         []any{"processes", "main", "exe"},
			wantRaw:      "${rpcbind_binary}",
			wantResolved: "/usr/bin/rpcbind",
			preflight:    "rpcbind-binary",
		},
		{
			name:         "rpc idmapd process uses app binary",
			service:      "rpc-idmapd",
			path:         []any{"processes", "main", "exe"},
			wantRaw:      "${rpc_idmapd_binary}",
			wantResolved: "/usr/bin/rpc.idmapd",
			preflight:    "rpc-idmapd-binary",
		},
		{
			name:         "nfs mountd process uses app binary",
			service:      "nfs",
			path:         []any{"processes", "mountd", "exe"},
			wantRaw:      "${rpc_mountd_binary}",
			wantResolved: "/usr/bin/rpc.mountd",
			preflight:    "rpc-mountd-binary",
		},
		{
			name:         "alloy config validation uses app binary",
			service:      "alloy",
			path:         []any{"preflight", "config", "command", 0},
			wantRaw:      "${alloy_binary}",
			wantResolved: "/usr/bin/alloy",
			preflight:    "alloy-binary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, ok := cfg.Daemons[tt.service]
			if !ok {
				t.Fatalf("service %q not found", tt.service)
			}
			if got := cfgval.String(valueAt(t, doc.Body, tt.path...)); got != tt.wantRaw {
				t.Fatalf("raw %s = %q, want %q", tt.service, got, tt.wantRaw)
			}
			resolved, errs := cfg.ResolveCatalog(CategoryService, tt.service)
			if len(errs) > 0 {
				t.Fatalf("ResolveCatalog(%s): %v", tt.service, errs)
			}
			if got := cfgval.String(valueAt(t, resolved.Tree, tt.path...)); got != tt.wantResolved {
				t.Fatalf("resolved %s = %q, want %q", tt.service, got, tt.wantResolved)
			}
			if tt.preflight == "" {
				return
			}
			preflight := nested(t, resolved.Tree, "preflight")
			entry, ok := preflight[tt.preflight].(map[string]any)
			if !ok {
				t.Fatalf("resolved %s lacks preflight %q: %v", tt.service, tt.preflight, preflight)
			}
			if got := cfgval.Bool(entry["optional"]); got != tt.preflightOptional {
				t.Fatalf("%s preflight %q optional = %v, want %v", tt.service, tt.preflight, got, tt.preflightOptional)
			}
		})
	}
}

func TestDatabaseCatalogDaemonsAreBackupToolNeutral(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  services: []\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	section := func(tree map[string]any, key string) map[string]any {
		out, _ := tree[key].(map[string]any)
		return out
	}

	for _, name := range []string{"mysql", "mariadb"} {
		doc, ok := cfg.Daemons[name]
		if !ok {
			t.Fatalf("service %q not found", name)
		}
		if apps := cfgval.StringList(doc.Body["apps"]); slices.Contains(apps, "mariadb-backup") {
			t.Fatalf("%s links mariadb-backup by default: %v", name, apps)
		}

		resolved, errs := cfg.ResolveCatalog(CategoryService, name)
		if len(errs) > 0 {
			t.Fatalf("ResolveCatalog(%s): %v", name, errs)
		}
		for _, tree := range []struct {
			label string
			body  map[string]any
		}{
			{label: "raw", body: doc.Body},
			{label: "resolved", body: resolved.Tree},
		} {
			if _, ok := section(tree.body, "checks")["mariadb-backup"]; ok {
				t.Fatalf("%s %s catalog still has mariadb-backup check", name, tree.label)
			}
			if _, ok := section(tree.body, "rules")["block-restart-during-backup"]; ok {
				t.Fatalf("%s %s catalog still has mariadb-backup guard", name, tree.label)
			}
			if _, ok := section(tree.body, "preflight")["mariadb-backup-binary"]; ok {
				t.Fatalf("%s %s catalog still has mariadb-backup preflight", name, tree.label)
			}
		}
	}
}

func TestWALGBackupAppsResolveRequiredBinaryPreflight(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  services: []\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	tests := []struct {
		name     string
		binaries []string
	}{
		{
			name:     "wal-g-mysql",
			binaries: []string{"/usr/bin/wal-g-mysql", "/usr/local/bin/wal-g-mysql", "/usr/bin/wal-g", "/usr/local/bin/wal-g"},
		},
		{
			name:     "wal-g-pg",
			binaries: []string{"/usr/bin/wal-g-pg", "/usr/local/bin/wal-g-pg", "/usr/bin/wal-g", "/usr/local/bin/wal-g"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, ok := cfg.Apps[tt.name]
			if !ok {
				t.Fatalf("app %q not found", tt.name)
			}
			vars, _ := doc.Body["variables"].(map[string]any)
			if got := cfgval.StringList(vars["binary"]); !slices.Equal(got, tt.binaries) {
				t.Fatalf("%s binary candidates = %v, want %v", tt.name, got, tt.binaries)
			}
			resolved, errs := cfg.ResolveCatalog(CategoryApp, tt.name)
			if len(errs) > 0 {
				t.Fatalf("ResolveCatalog(%s): %v", tt.name, errs)
			}
			binary := cfgval.String(valueAt(t, resolved.Tree, "variables", "binary"))
			if !slices.Contains(tt.binaries, binary) {
				t.Fatalf("%s resolved binary = %q, want one of %v", tt.name, binary, tt.binaries)
			}
			preflight := nested(t, resolved.Tree, "preflight")
			binaryPreflight, ok := preflight["binary"].(map[string]any)
			if !ok {
				t.Fatalf("%s lacks preflight \"binary\": %v", tt.name, preflight)
			}
			if got := cfgval.Bool(binaryPreflight["optional"]); got {
				t.Fatalf("%s preflight \"binary\" optional = %v, want false", tt.name, got)
			}

			versionPreflight, ok := preflight["version"].(map[string]any)
			if !ok {
				t.Fatalf("%s lacks preflight \"version\": %v", tt.name, preflight)
			}
			if got := cfgval.Bool(versionPreflight["optional"]); got {
				t.Fatalf("%s preflight \"version\" optional = %v, want false", tt.name, got)
			}
			versionCommand, ok := nested(t, preflight, "version")["command"].([]any)
			if !ok || len(versionCommand) == 0 {
				t.Fatalf("%s version command missing: %v", tt.name, preflight["version"])
			}
			if got := cfgval.String(versionCommand[0]); got != binary {
				t.Fatalf("%s version command binary = %q, want %q", tt.name, got, binary)
			}
		})
	}
}

func TestCatalogServicesReuseLinkedAppBinaries(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + catalogDir + "]\n  services: []\n" +
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
			t.Errorf("%s defines binary %q already owned by app %s; use ${%s_binary} instead", name, serviceBinary, appName, appVariablePrefix(appName))
			if hasVersionProbe(doc.Body) {
				t.Errorf("%s defines a service-level version probe already owned by app %s", name, appName)
			}
		}
	}
}

func TestCatalogServicesDoNotOwnRuntimeResourcePreflight(t *testing.T) {
	root := repoRoot(t)
	files, err := yamlFiles(filepath.Join(root, "catalog", "services"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		path := filepath.Join(root, "catalog", "services", file)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		preflight, _ := doc["preflight"].(map[string]any)
		for name, raw := range preflight {
			entry, _ := raw.(map[string]any)
			switch cfgval.String(entry["type"]) {
			case "binary", "libraries":
				t.Errorf("%s preflight.%s uses runtime resource type %q; move it to catalog/apps", path, name, entry["type"])
			}
		}
	}
}

func TestCatalogVersionedServicesDiscoverFromLinkedApps(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")

	apps := map[string]map[string]any{}
	appFiles, err := yamlFiles(filepath.Join(catalogDir, "apps"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range appFiles {
		path := filepath.Join(catalogDir, "apps", file)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if name := cfgval.String(doc["name"]); name != "" {
			apps[name] = doc
		}
	}

	serviceFiles, err := yamlFiles(filepath.Join(catalogDir, "services"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range serviceFiles {
		path := filepath.Join(catalogDir, "services", file)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if _, hasVersions := doc["versions"]; hasVersions {
			t.Errorf("%s declares versions; service templates must discover from linked apps", path)
		}
		tok := tokenFor(cfgval.String(doc["name"]))
		if tok == nil {
			continue
		}
		hasLinkedDiscovery := false
		for _, appName := range cfgval.StringList(doc["apps"]) {
			app, ok := apps[linkedAppTemplateName(appName, *tok)]
			if !ok {
				continue
			}
			if anyContains(directVersionDiscoverySources(app), tok.marker()) {
				hasLinkedDiscovery = true
				break
			}
		}
		if !hasLinkedDiscovery {
			t.Errorf("%s is a template but does not link an app template that can discover %s", path, tok.marker())
		}
	}
}

func TestCatalogCommandEntriesDoNotUseArgumentKeys(t *testing.T) {
	root := repoRoot(t)
	catalogDir := filepath.Join(root, "catalog")
	err := filepath.WalkDir(catalogDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !isYAML(entry.Name()) {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // test walks YAML files under the repository catalog root.
		if err != nil {
			return err
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		checkCommandArgumentKeys(t, path, doc, "")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func checkCommandArgumentKeys(t *testing.T, file string, node any, keyPath string) {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		if cfgval.String(v["type"]) == "command" {
			for key := range v {
				if strings.HasPrefix(key, "-") {
					t.Errorf("%s %s has command argument key %q outside command list", file, keyPath, key)
				}
			}
		}
		for key, child := range v {
			next := key
			if keyPath != "" {
				next = keyPath + "." + key
			}
			checkCommandArgumentKeys(t, file, child, next)
		}
	case []any:
		for _, child := range v {
			checkCommandArgumentKeys(t, file, child, keyPath+"[]")
		}
	}
}

func catalogBinary(doc *Document) string {
	if doc == nil {
		return ""
	}
	return DocumentBinary(doc.Body)
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

func hasHealthProbe(body map[string]any) bool {
	preflight, _ := body["preflight"].(map[string]any)
	if preflight == nil {
		return false
	}
	_, ok := preflight["health"]
	return ok
}

func versionProbeOptional(body map[string]any) bool {
	preflight, _ := body["preflight"].(map[string]any)
	if preflight == nil {
		return false
	}
	version, _ := preflight["version"].(map[string]any)
	if version == nil {
		return false
	}
	return cfgval.Bool(version["optional"])
}

func catalogAppProvidesVersion(cfg *Config, name string, seen map[string]bool) bool {
	if seen[name] {
		return false
	}
	seen[name] = true
	doc, ok := cfg.Apps[name]
	if !ok {
		return false
	}
	if hasVersionProbe(doc.Body) {
		return true
	}
	source := cfgval.String(doc.Body["version_from"])
	if source == "" {
		return false
	}
	return catalogAppProvidesVersion(cfg, source, seen)
}

func valueAt(t *testing.T, tree map[string]any, path ...any) any {
	t.Helper()
	var cur any = tree
	for _, elem := range path {
		switch key := elem.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				t.Fatalf("path %v: expected map before key %q, got %T", path, key, cur)
			}
			var found bool
			cur, found = m[key]
			if !found {
				t.Fatalf("path %v: key %q not found", path, key)
			}
		case int:
			a, ok := cur.([]any)
			if !ok {
				t.Fatalf("path %v: expected array before index %d, got %T", path, key, cur)
			}
			if key < 0 || key >= len(a) {
				t.Fatalf("path %v: index %d out of range", path, key)
			}
			cur = a[key]
		default:
			t.Fatalf("path %v: unsupported path element %T", path, elem)
		}
	}
	return cur
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

func assertExampleDocsHaveKind(t *testing.T, dir, kind string) {
	t.Helper()
	if !dirExists(dir) {
		return
	}
	files, err := yamlFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		doc, err := loadDocument(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if doc.Kind != kind {
			t.Fatalf("%s must use kind: %s, got %q", doc.Path, kind, doc.Kind)
		}
	}
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
