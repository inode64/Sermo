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

func TestShippedServiceConfigExamplesValidate(t *testing.T) {
	root := repoRoot(t)
	servicesDir := filepath.Join(root, "configs", "services")
	if !dirExists(servicesDir) {
		t.Fatalf("configs/services is missing")
	}

	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  catalog: [" + filepath.Join(root, "catalog") + "]\n  includes: [" + servicesDir + "]\n  runtime: /run/sermo\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Services) == 0 {
		t.Fatalf("configs/services has no loadable service examples")
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("configs/services examples must validate cleanly, got: %v", issues)
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
		"paths:\n  catalog: [" + filepath.Join(root, "catalog") + "]\n  includes: [" + enabled + "]\n  runtime: /run/sermo\n" +
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
	body := "paths:\n  catalog: [" + filepath.Join(root, "catalog") + "]\n  includes: []\n" +
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
		"automount":    {"autofs", "automount"},
		"avahi":        {"avahi", "avahi-daemon"},
		"cups":         {"cupsd"},
		"dbus":         {"dbus", "dbus-daemon"},
		"fail2ban":     {"fail2ban", "fail2ban-server"},
		"in.tftpd":     {"in.tftpd", "in-tftpd"},
		"keydb":        {"keydb", "keydb-server"},
		"qemu-ga":      {"qemu-guest-agent", "qemu-ga"},
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
	body := "paths:\n  catalog: [" + catalogDir + "]\n  includes: []\n" +
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
	body := "paths:\n  catalog: [" + catalogDir + "]\n  includes: []\n" +
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
	body := "paths:\n  catalog: [" + catalogDir + "]\n  includes: []\n"
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
			if got := cfgval.StringList(doc.Body["binary"]); !slices.Equal(got, tt.binaries) {
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
			if got := cfgval.Bool(versionPreflight["optional"]); !got {
				t.Fatalf("%s preflight \"version\" optional = %v, want true", tt.name, got)
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
			t.Errorf("%s defines binary %q already owned by app %s; use ${%s_binary} instead", name, serviceBinary, appName, appVariablePrefix(appName))
			if hasVersionProbe(doc.Body) {
				t.Errorf("%s defines a service-level version probe already owned by app %s", name, appName)
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
