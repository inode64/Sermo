package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/cfgval"
)

func TestMaterializedTemplateMatchesUsesAllBinaryCandidates(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	makeVersionedBinaries(t, second, "php", "php-fpm", "8.2", "8.3")

	tok := tokenFor("php-fpm%v")
	if tok == nil {
		t.Fatal("missing version token")
	}
	paths := []string{
		filepath.Join(first, "php${version}", "bin", "php-fpm"),
		filepath.Join(second, "php${version}", "bin", "php-fpm"),
	}
	got := materializedTemplateMatches(paths, false, nil, []tmplToken{*tok})
	want := []string{"8.2", "8.3"}
	if values := templateMatchValues(got, "version"); strings.Join(values, ",") != strings.Join(want, ",") {
		t.Fatalf("materializedTemplateMatches = %v, want %v", values, want)
	}
}

func TestMaterializedTemplateMatchesDedupesSameTupleAcrossSources(t *testing.T) {
	root := t.TempDir()
	etcSystemdDir := filepath.Join(root, "etc", "systemd", "system")
	libSystemdDir := filepath.Join(root, "usr", "lib", "systemd", "system")
	if err := os.MkdirAll(etcSystemdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(libSystemdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{
		filepath.Join(etcSystemdDir, "php-fpm@8.2.service"),
		filepath.Join(libSystemdDir, "php-fpm@8.2.service"),
	} {
		if err := os.WriteFile(file, []byte("[Service]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	paths := []string{
		filepath.Join(etcSystemdDir, "php-fpm@${version}${sep}${instance}.service"),
		filepath.Join(libSystemdDir, "php-fpm@${version}${sep}${instance}.service"),
	}
	got := materializedTemplateMatches(paths, false, nil, tokensFor("php-fpm%v%s%i"))
	if len(got) != 1 {
		t.Fatalf("materializedTemplateMatches returned %d matches, want one: %#v", len(got), got)
	}
	if got[0].values["version"] != "8.2" || got[0].values["sep"] != "" || got[0].values["instance"] != "" {
		t.Fatalf("materializedTemplateMatches values = %v, want version 8.2 with empty sep/instance", got[0].values)
	}
	if got[0].matchedPath != filepath.Join(etcSystemdDir, "php-fpm@8.2.service") {
		t.Fatalf("materializedTemplateMatches kept %q, want first unit source", got[0].matchedPath)
	}
}

func TestMaterializedServiceUnitMatchesOptionalInstanceFromVersionCandidate(t *testing.T) {
	toks := tokensFor("php-fpm%v%s%i")
	patterns := serviceUnitPatternsForBackend("systemd", []string{
		"php-fpm${version}",
	}, toks)
	got := materializedServiceUnitMatches(patterns, []string{"php-fpm8.3.service"}, toks)
	if len(got) != 1 {
		t.Fatalf("materializedServiceUnitMatches returned %d matches, want one: %#v", len(got), got)
	}
	if got[0].values["version"] != "8.3" || got[0].values["sep"] != "" || got[0].values["instance"] != "" {
		t.Fatalf("materializedServiceUnitMatches values = %v, want version 8.3 with empty sep/instance", got[0].values)
	}
	if got[0].matchedPath != "php-fpm8.3.service" {
		t.Fatalf("materializedServiceUnitMatches matched path = %q", got[0].matchedPath)
	}
}

func TestVersionTemplateDiscoverySelectsActiveInitSources(t *testing.T) {
	root := t.TempDir()
	systemdDir := filepath.Join(root, "usr", "lib", "systemd", "system")
	openrcDir := filepath.Join(root, "etc", "init.d")
	for _, dir := range []string{systemdDir, openrcDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{
		filepath.Join(systemdDir, "svc@2.0.service"),
		filepath.Join(openrcDir, "svc-3.0"),
	} {
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	load := func(t *testing.T, backend string) *Config {
		t.Helper()
		catalogDir := filepath.Join(root, "catalog-"+backend, "services")
		servicesDir := filepath.Join(root, "enabled-"+backend)
		if err := os.MkdirAll(catalogDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(servicesDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(catalogDir, "svc.yml"), fmt.Appendf(nil, `
name: svc%%v
service: svc${version}
versions:
  from:
    systemd: %s/svc@${version}.service
    openrc: %s/svc-${version}
checks:
  service: { type: service, expect: active }
`, systemdDir, openrcDir), 0o644); err != nil {
			t.Fatal(err)
		}
		global := filepath.Join(root, "sermo-"+backend+".yml")
		if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: %s }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, backend, servicesDir), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig(t, global, WithCatalogDirs(filepath.Dir(catalogDir)))
		if err != nil {
			t.Fatalf("Load(%s): %v", backend, err)
		}
		return cfg
	}

	systemd := load(t, "systemd")
	if _, ok := systemd.CatalogServices["svc2.0"]; !ok {
		t.Fatal("systemd discovery missing svc2.0")
	}
	if _, ok := systemd.CatalogServices["svc3.0"]; ok {
		t.Fatal("systemd discovery must not use OpenRC versions.from")
	}
	if _, ok := systemd.CatalogServices["svc1.0"]; ok {
		t.Fatal("systemd discovery must not use a shared default versions.from branch")
	}

	openrc := load(t, "openrc")
	if _, ok := openrc.CatalogServices["svc3.0"]; !ok {
		t.Fatal("openrc discovery missing svc3.0")
	}
	if _, ok := openrc.CatalogServices["svc2.0"]; ok {
		t.Fatal("openrc discovery must not use systemd versions.from")
	}
	if _, ok := openrc.CatalogServices["svc1.0"]; ok {
		t.Fatal("openrc discovery must not use a shared default versions.from branch")
	}

	t.Run("env backend override", func(t *testing.T) {
		t.Setenv("SERMO_BACKEND", "openrc")
		cfg := load(t, "auto")
		if _, ok := cfg.CatalogServices["svc3.0"]; !ok {
			t.Fatal("SERMO_BACKEND=openrc should select OpenRC versions.from")
		}
		if _, ok := cfg.CatalogServices["svc2.0"]; ok {
			t.Fatal("SERMO_BACKEND=openrc must not select systemd versions.from")
		}
	})
}

func templateMatchValues(matches []templateMatch, variable string) []string {
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.values[variable])
	}
	return out
}

// TestCatalogServiceVersionTemplateDiscoversFromLinkedApp covers a catalog service template whose
// monitored binary is generic (no ${version}); installed versions come from the
// linked app template, and ${version} is baked into the service body.
func TestCatalogServiceVersionTemplateDiscoversFromLinkedApp(t *testing.T) {
	root := t.TempDir()
	slots := filepath.Join(root, "lib")
	makeVersionedBinaries(t, slots, "php", "php-fpm", "7.4", "8.3")

	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(catalogDir, "apps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(catalogDir, "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	appTmpl := fmt.Sprintf(`
name: php-fpm%%v
display_name: "PHP-FPM ${version}"
versions:
  from: "%s/php${version}/bin/php-fpm"
variables:
  binary: "%s/php${version}/bin/php-fpm"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "-v"] }
`, slots, slots)
	if err := os.WriteFile(filepath.Join(catalogDir, "apps", "php-fpm.yml"), []byte(appTmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl := `
name: php-fpm%v
display_name: "PHP-FPM ${version}"
service:
  systemd: ["php${version}-fpm"]
apps: ["php-fpm${version}"]
variables:
  binary: /usr/sbin/php-fpm
`
	if err := os.WriteFile(filepath.Join(catalogDir, "services", "php-fpm.yml"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: openrc }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, v := range []string{"7.4", "8.3"} {
		doc, ok := cfg.CatalogServices["php-fpm"+v]
		if !ok {
			t.Fatalf("expected materialized service php-fpm%s", v)
		}
		// Generic binary is preserved; version did not leak into it.
		if got := DocumentBinary(doc.Body); got != "/usr/sbin/php-fpm" {
			t.Errorf("php-fpm%s binary = %q, want /usr/sbin/php-fpm", v, got)
		}
		// ${version} baked into the service unit candidate.
		sysd := nested(t, doc.Body, "service")["systemd"].([]any)
		if got := sysd[0].(string); got != "php"+v+"-fpm" {
			t.Errorf("php-fpm%s service unit = %q, want php%s-fpm", v, got, v)
		}
		// Discovery metadata stripped from the concrete service.
		if _, present := doc.Body["versions"]; present {
			t.Errorf("php-fpm%s still carries versions block", v)
		}
	}
}

func TestCatalogServiceVersionTemplateRequiresLinkedAppDiscovery(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "worker1.0"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/services/worker.yml": fmt.Sprintf(`
name: worker%%v
variables:
  binary: "%s/worker${version}"
checks: { service: { type: service, expect: active } }
`, bin),
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.CatalogServices["worker1.0"]; ok {
		t.Fatalf("service template materialized from service binary; expected linked app discovery only")
	}
}

func TestTomcatVersionTemplateLinksMaterializedApp(t *testing.T) {
	root := t.TempDir()
	tomcatRoot := filepath.Join(root, "usr", "share")
	makeVersionedBinaries(t, tomcatRoot, "tomcat-", "catalina.sh", "9", "10")

	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	catalina := filepath.Join(tomcatRoot, "tomcat-${version}", "bin", "catalina.sh")
	writeFile(t, filepath.Join(catalogDir, "apps"), "java.yml", `
name: java
variables:
  binary: /usr/bin/java
preflight:
  binary: { type: binary, path: "${binary}" }
`)
	writeFile(t, filepath.Join(catalogDir, "apps"), "tomcat.yml", fmt.Sprintf(`
name: tomcat-%%v
display_name: "Apache Tomcat ${version}"
variables:
  binary: %q
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "version"], timeout: 10s }
`, catalina))
	writeFile(t, filepath.Join(catalogDir, "services"), "tomcat.yml", `
name: tomcat-%v
display_name: "Apache Tomcat ${version}"
service: tomcat
apps: [java, "tomcat-${version}"]
variables: { port: 8080 }
checks: { service: { type: service, expect: active } }
`)
	writeFile(t, servicesDir, "site.yml", "name: site\nuses: tomcat-10\n")

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: systemd }
paths:
  services: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.Apps["tomcat-%v"]; ok {
		t.Fatalf("app template tomcat-%%v should not be registered")
	}
	for _, v := range []string{"9", "10"} {
		if _, ok := cfg.Apps["tomcat-"+v]; !ok {
			t.Fatalf("expected materialized app tomcat-%s", v)
		}
		if _, ok := cfg.CatalogServices["tomcat-"+v]; !ok {
			t.Fatalf("expected materialized service tomcat-%s", v)
		}
	}

	resolved, errs := cfg.Resolve("site")
	if len(errs) != 0 {
		t.Fatalf("Resolve(site) errors = %v", errs)
	}
	preflight := nested(t, resolved.Tree, "preflight")
	if got := cfgval.String(nested(t, preflight, "java-binary")["path"]); got != "/usr/bin/java" {
		t.Fatalf("java-binary path = %q, want /usr/bin/java", got)
	}
	wantCatalina := filepath.Join(tomcatRoot, "tomcat-10", "bin", "catalina.sh")
	if got := cfgval.String(nested(t, preflight, "tomcat-10-binary")["path"]); got != wantCatalina {
		t.Fatalf("tomcat-10-binary path = %q, want %q", got, wantCatalina)
	}
	version := nested(t, preflight, "tomcat-10-version")
	command, _ := version["command"].([]any)
	if len(command) != 2 || command[0] != wantCatalina || command[1] != "version" {
		t.Fatalf("tomcat-10-version command = %v, want [%s version]", command, wantCatalina)
	}
}

func TestVersionTemplateServiceLinksMaterializedApp(t *testing.T) {
	root := t.TempDir()
	pgRoot := filepath.Join(root, "usr", "lib64")
	makeVersionedBinaries(t, pgRoot, "postgresql-", "postgres", "15", "16")

	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	binary := filepath.Join(pgRoot, "postgresql-${version}", "bin", "postgres")
	writeFile(t, filepath.Join(catalogDir, "apps"), "postgres.yml", fmt.Sprintf(`
name: postgres-%%v
display_name: "PostgreSQL ${version}"
variables:
  binary: %q
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "--version"], timeout: 10s }
`, binary))
	writeFile(t, filepath.Join(catalogDir, "services"), "postgres.yml", `
name: postgres-%v
display_name: "PostgreSQL ${version}"
service: "postgresql-${version}"
apps: ["postgres-${version}"]
variables:
  port: 5432
  data_dir: /var/lib/postgresql/${version}/data
pidfile: "${data_dir}/postmaster.pid"
checks: { service: { type: service, expect: active } }
`)
	writeFile(t, servicesDir, "pg.yml", "name: pg\nuses: postgres-16\n")

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: auto }
paths:
  services: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global, withServiceUnits("systemd", []string{"postgresql-16.service"}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.Apps["postgres-%v"]; ok {
		t.Fatalf("app template postgres-%%v should not be registered")
	}
	for _, v := range []string{"15", "16"} {
		if _, ok := cfg.Apps["postgres-"+v]; !ok {
			t.Fatalf("expected materialized app postgres-%s", v)
		}
	}
	if _, ok := cfg.CatalogServices["postgres-16"]; !ok {
		t.Fatal("expected materialized service postgres-16 from active service unit")
	}
	if _, ok := cfg.CatalogServices["postgres-15"]; ok {
		t.Fatal("postgres-15 service must not materialize without an active service unit")
	}

	resolved, errs := cfg.Resolve("pg")
	if len(errs) != 0 {
		t.Fatalf("Resolve(pg) errors = %v", errs)
	}
	if _, ok := resolved.Tree["apps"]; ok {
		t.Fatalf("apps should be consumed during resolution: %v", resolved.Tree["apps"])
	}
	preflight := nested(t, resolved.Tree, "preflight")
	binaryCheck := nested(t, preflight, "postgres-16-binary")
	wantBinary := filepath.Join(pgRoot, "postgresql-16", "bin", "postgres")
	if got := cfgval.String(binaryCheck["path"]); got != wantBinary {
		t.Fatalf("postgres-16-binary path = %q, want %q", got, wantBinary)
	}
	versionCheck := nested(t, preflight, "postgres-16-version")
	command, _ := versionCheck["command"].([]any)
	if len(command) != 2 || command[0] != wantBinary || command[1] != "--version" {
		t.Fatalf("postgres-16-version command = %v, want [%s --version]", command, wantBinary)
	}
	if got := cfgval.String(resolved.Tree["pidfile"]); got != "/var/lib/postgresql/16/data/postmaster.pid" {
		t.Fatalf("postgres-16 pidfile = %q, want /var/lib/postgresql/16/data/postmaster.pid", got)
	}
}

func TestVersionTemplateDiscoversFromLinkedAppTemplate(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"php-fpm8.2", "php-fpm8.4"} {
		if err := os.WriteFile(filepath.Join(bin, f), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	catalogDir := filepath.Join(root, "catalog")
	appsDir := filepath.Join(catalogDir, "apps")
	catalogServicesDir := filepath.Join(catalogDir, "services")
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{appsDir, catalogServicesDir, servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(appsDir, "php-fpm.yml"), fmt.Appendf(nil, `
name: php-fpm%%v
display_name: "PHP-FPM ${version}"
variables:
  binary: "%s/php-fpm${version}"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "-v"] }
`, bin), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogServicesDir, "php-fpm.yml"), []byte(`
name: php-fpm%v
display_name: "PHP-FPM ${version}"
apps: ["php-fpm${version}"]
preflight:
  config: { type: command, command: ["${binary}", "--test"] }
processes:
  main: { exe: "${binary}", user: root }
checks:
  service: { type: service, expect: active }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: auto }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, version := range []string{"8.2", "8.4"} {
		name := "php-fpm" + version
		if _, ok := cfg.CatalogServices[name]; !ok {
			t.Fatalf("expected materialized service %s", name)
		}
		if _, ok := cfg.Apps[name]; !ok {
			t.Fatalf("expected materialized app %s", name)
		}
		resolved, errs := cfg.ResolveCatalog(CategoryService, name)
		if len(errs) != 0 {
			t.Fatalf("ResolveCatalog(%s) errors = %v", name, errs)
		}
		wantBinary := filepath.Join(bin, name)
		configCmd, _ := nested(t, nested(t, resolved.Tree, "preflight"), "config")["command"].([]any)
		if got := fmt.Sprint(configCmd...); got != wantBinary+"--test" {
			t.Fatalf("%s config command = %v, want linked app binary", name, configCmd)
		}
		main := nested(t, resolved.Tree, "processes", "main")
		if got := cfgval.String(main["exe"]); got != wantBinary {
			t.Fatalf("%s process exe = %q, want %q", name, got, wantBinary)
		}
	}
}

func TestVersionTemplateUnversionedMaterialization(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"python", "python3", "php", "php8.4"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	catalogDir := filepath.Join(root, "catalog")
	appsDir := filepath.Join(catalogDir, "apps")
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{appsDir, servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(appsDir, "python%n.yml"), fmt.Appendf(nil, `
name: python%%n
display_name: "Python ${n}"
description: "Python runtime ${n}"
variables:
  binary: "%s/python${n}"
preflight:
  binary: { type: binary, path: "${binary}" }
`, bin), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appsDir, "php.yml"), fmt.Appendf(nil, `
name: php%%v
display_name: "PHP ${version}"
description: "PHP runtime ${version}"
variables:
  binary: "%s/php${version}"
preflight:
  binary: { type: binary, path: "${binary}" }
`, bin), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: auto }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	assertUnversionedVersionTemplate(t, cfg, bin)
}

func assertUnversionedVersionTemplate(t *testing.T, cfg *Config, bin string) {
	t.Helper()
	if got := strings.Join(cfg.CatalogNamesInCategory(CategoryApp), ","); got != "php,php8.4,python,python3" {
		t.Fatalf("app names = %s, want php,php8.4,python,python3", got)
	}
	tests := []struct {
		name        string
		binary      string
		displayName string
		description string
	}{
		{"python", filepath.Join(bin, "python"), "Python", "Python runtime"},
		{"python3", filepath.Join(bin, "python3"), "Python 3", "Python runtime 3"},
		{"php", filepath.Join(bin, "php"), "PHP", "PHP runtime"},
		{"php8.4", filepath.Join(bin, "php8.4"), "PHP 8.4", "PHP runtime 8.4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, ok := cfg.Apps[tt.name]
			if !ok {
				t.Fatalf("app %q was not materialized", tt.name)
			}
			if _, present := doc.Body["versions"]; present {
				t.Fatalf("%s still carries versions block", tt.name)
			}
			if got := DocumentBinary(doc.Body); got != tt.binary {
				t.Fatalf("%s binary = %q, want %q", tt.name, got, tt.binary)
			}
			if got := DisplayName(doc.Body, tt.name); got != tt.displayName {
				t.Fatalf("%s display_name = %q, want %q", tt.name, got, tt.displayName)
			}
			if got := cfgval.String(doc.Body["description"]); got != tt.description {
				t.Fatalf("%s description = %q, want %q", tt.name, got, tt.description)
			}
			resolved, errs := cfg.ResolveCatalog(CategoryApp, tt.name)
			if len(errs) > 0 {
				t.Fatalf("ResolveCatalog(%s): %v", tt.name, errs)
			}
			preflight := nested(t, resolved.Tree, "preflight", "binary")
			if got := cfgval.String(preflight["path"]); got != tt.binary {
				t.Fatalf("%s resolved binary path = %q, want %q", tt.name, got, tt.binary)
			}
		})
	}
}

// Berkeley DB ships one binary per subcommand and no bare db5.3, so a trailing
// %v used to capture "5.3_archive", "5.3_dump", ... and materialize one app per
// subcommand. versions.suffix trims the subcommand back to the release.
func TestVersionTemplateSuffixCollapsesSubcommandBinaries(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two releases installed side by side, each as a family of subcommands.
	for _, name := range []string{
		"db5.3_archive", "db5.3_dump", "db5.3_stat", "db5.3_tuner",
		"db6.2_archive", "db6.2_dump",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	appsDir := filepath.Join(root, "catalog", "apps")
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{appsDir, servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(appsDir, "db.yml"), fmt.Appendf(nil, `
name: db%%v
display_name: "Berkeley DB ${version}"
versions:
  from: "%[1]s/db${version}"
  suffix: "_*"
variables:
  binary:
    - "%[1]s/db${version}_dump"
    - "%[1]s/db${version}_stat"
preflight:
  binary: { type: binary, path: "${binary}" }
`, bin), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: auto }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := strings.Join(cfg.CatalogNamesInCategory(CategoryApp), ","); got != "db5.3,db6.2" {
		t.Fatalf("app names = %s, want db5.3,db6.2 (one per release, not per subcommand)", got)
	}
	for name, wantBinary := range map[string]string{
		"db5.3": filepath.Join(bin, "db5.3_dump"),
		"db6.2": filepath.Join(bin, "db6.2_dump"),
	} {
		doc, ok := cfg.Apps[name]
		if !ok {
			t.Fatalf("app %q was not materialized", name)
		}
		if got := DisplayName(doc.Body, name); got != "Berkeley DB "+strings.TrimPrefix(name, "db") {
			t.Fatalf("%s display_name = %q", name, got)
		}
		// The pinned candidate list wins: discovery came from versions.from, so
		// the probe is not rebaked to whichever subcommand globbed first.
		resolved, errs := cfg.ResolveCatalog(CategoryApp, name)
		if len(errs) > 0 {
			t.Fatalf("ResolveCatalog(%s): %v", name, errs)
		}
		preflight := nested(t, resolved.Tree, "preflight", "binary")
		if got := cfgval.String(preflight["path"]); got != wantBinary {
			t.Fatalf("%s probe binary = %q, want %q", name, got, wantBinary)
		}
	}
}

func TestVersionTemplateCurrentMarker(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	sbin := filepath.Join(root, "sbin")
	for _, dir := range []string{bin, sbin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeBin := func(dir, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	linkBin := func(dir, link, target string) {
		t.Helper()
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}

	for _, name := range []string{"php8.1", "php8.2", "python2", "python3"} {
		writeBin(bin, name)
	}
	for _, name := range []string{"php-fpm8.2", "php-fpm8.3"} {
		writeBin(sbin, name)
	}
	linkBin(bin, "php", "php8.2")
	linkBin(bin, "python", "python3")
	linkBin(sbin, "php-fpm", "php-fpm8.3")

	catalogDir := filepath.Join(root, "catalog")
	appsDir := filepath.Join(catalogDir, "apps")
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{appsDir, servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeApp := func(file, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(appsDir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeApp("php.yml", fmt.Sprintf(`
name: php%%v
display_name: "PHP ${version} ${current}"
variables:
  binary: "%s/php${version}"
`, bin))
	writeApp("php-fpm.yml", fmt.Sprintf(`
name: php-fpm%%v
display_name: "PHP-FPM ${version} ${current}"
variables:
  binary:
    - "%s/missing/php-fpm${version}"
    - "%s/php-fpm${version}"
`, root, sbin))
	writeApp("python%n.yml", fmt.Sprintf(`
name: python%%n
display_name: "Python ${n} ${current}"
variables:
  binary: "%s/python${n}"
`, bin))
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: auto }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	tests := []struct {
		name string
		want string
	}{
		{"php", "PHP"},
		{"php8.1", "PHP 8.1"},
		{"php8.2", "PHP 8.2 current"},
		{"php-fpm", "PHP-FPM"},
		{"php-fpm8.2", "PHP-FPM 8.2"},
		{"php-fpm8.3", "PHP-FPM 8.3 current"},
		{"python", "Python"},
		{"python2", "Python 2"},
		{"python3", "Python 3 current"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, ok := cfg.Apps[tt.name]
			if !ok {
				t.Fatalf("app %q was not materialized", tt.name)
			}
			if got := DisplayName(doc.Body, tt.name); got != tt.want {
				t.Fatalf("%s display_name = %q, want %q", tt.name, got, tt.want)
			}
			if resolved, errs := cfg.ResolveCatalog(CategoryApp, tt.name); len(errs) > 0 {
				t.Fatalf("ResolveCatalog(%s) = %+v, %v", tt.name, resolved, errs)
			}
		})
	}
}

func TestJavaVersionTemplateDiscoversFullVersionsFromJVMDirectory(t *testing.T) {
	root := t.TempDir()
	jvm := filepath.Join(root, "usr", "lib", "jvm")
	opt := filepath.Join(root, "opt")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{jvm, opt, bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeJavaHome := func(name, releaseVersion string) string {
		t.Helper()
		home := filepath.Join(opt, name)
		if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "bin", "java"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "release"), []byte(`JAVA_VERSION="`+releaseVersion+`"`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return home
	}
	java21 := writeJavaHome("openjdk-bin-21.0.11_p10", "21.0.11")
	java25 := writeJavaHome("openjdk-bin-25.0.3_p9", "25.0.3")
	links := map[string]string{
		"openjdk-bin-21":          java21,
		"openjdk-bin-21.0.11_p10": java21,
		"openjdk-bin-25":          java25,
		"openjdk-bin-25.0.3_p9":   java25,
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(jvm, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(jvm, "openjdk-bin-25", "bin", "java"), filepath.Join(bin, "java")); err != nil {
		t.Fatal(err)
	}

	catalogDir := filepath.Join(root, "catalog")
	appsDir := filepath.Join(catalogDir, "apps")
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{appsDir, servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeApp := func(file, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(appsDir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeApp("java.yml", fmt.Sprintf(`
name: java-%%i-%%v
display_name: "Java ${instance} ${version} ${current}"
versions:
  current_from: "%s/java"
variables:
  binary:
    - "%s/${instance}-jre-bin-${version}/bin/java"
    - "%s/${instance}-jdk-bin-${version}/bin/java"
    - "%s/${instance}-bin-${version}/bin/java"
    - "%s/${instance}-${version}/bin/java"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "-version"], timeout: 10s }
`, bin, jvm, jvm, jvm, jvm))
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: auto }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	assertJavaVersionTemplate(t, cfg, bin, jvm)
}

func assertJavaVersionTemplate(t *testing.T, cfg *Config, bin, jvm string) {
	t.Helper()
	tests := []struct {
		name        string
		binary      string
		displayName string
	}{
		{
			name:        "java",
			binary:      filepath.Join(bin, "java"),
			displayName: "Java",
		},
		{
			name:        "java-openjdk-21.0.11_p10",
			binary:      filepath.Join(jvm, "openjdk-bin-21", "bin", "java"),
			displayName: "Java openjdk 21.0.11_p10",
		},
		{
			name:        "java-openjdk-25.0.3_p9",
			binary:      filepath.Join(jvm, "openjdk-bin-25", "bin", "java"),
			displayName: "Java openjdk 25.0.3_p9 current",
		},
	}
	if _, ok := cfg.Apps["java-openjdk-21"]; ok {
		t.Fatalf("short Java version should be deduplicated")
	}
	if _, ok := cfg.Apps["java-openjdk-25"]; ok {
		t.Fatalf("short Java version should be deduplicated")
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, ok := cfg.Apps[tt.name]
			if !ok {
				t.Fatalf("app %q was not materialized", tt.name)
			}
			if got := DisplayName(doc.Body, tt.name); got != tt.displayName {
				t.Fatalf("%s display_name = %q, want %q", tt.name, got, tt.displayName)
			}
			if got := DocumentBinary(doc.Body); got != tt.binary {
				t.Fatalf("%s binary = %q, want %q", tt.name, got, tt.binary)
			}
			resolved, errs := cfg.ResolveCatalog(CategoryApp, tt.name)
			if len(errs) > 0 {
				t.Fatalf("ResolveCatalog(%s): %v", tt.name, errs)
			}
			if got := cfgval.String(valueAt(t, resolved.Tree, "variables", "binary")); got != tt.binary {
				t.Fatalf("%s resolved binary = %q, want %q", tt.name, got, tt.binary)
			}
		})
	}
}

func TestCompositeVersionTemplateCurrentFromMaterializesActiveSlot(t *testing.T) {
	cfg, err := loadConfig(t, writeConfig(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/java.yml": `
name: java-%i-%v
display_name: "Java ${instance} ${version}"
versions:
  current_from: /usr/bin/java
preflight:
  binary: { type: binary, path: "${binary}" }
`,
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	doc, ok := cfg.Apps["java"]
	if !ok {
		t.Fatalf("current_from did not materialize active app: %v", cfg.AppNames)
	}
	if got := DisplayName(doc.Body, "java"); got != "Java" {
		t.Fatalf("java display_name = %q, want Java", got)
	}
	if got := DocumentBinary(doc.Body); got != "/usr/bin/java" {
		t.Fatalf("java binary = %q, want /usr/bin/java", got)
	}
	if _, ok := cfg.Apps["java--"]; ok {
		t.Fatalf("active app was materialized with dangling separators")
	}
}

func TestVersionTemplateUnversionedRequiresBinary(t *testing.T) {
	bin := makeBinDir(t, "python3")
	cfg := loadCatalog(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/python%n.yml": fmt.Sprintf(`
name: python%%n
display_name: "Python ${n}"
variables:
  binary: "%s/python${n}"
`, bin),
	})
	if _, ok := cfg.Apps["python"]; ok {
		t.Fatalf("python should not materialize without %s", filepath.Join(bin, "python"))
	}
	if _, ok := cfg.Apps["python3"]; !ok {
		t.Fatalf("python3 should materialize")
	}
}

func TestVersionTemplateUnversionedCanBeDisabled(t *testing.T) {
	bin := makeBinDir(t, "php", "php8.4")
	cfg := loadCatalog(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/php.yml": fmt.Sprintf(`
name: php%%v
display_name: "PHP ${version}"
versions:
  unversioned: false
variables:
  binary: "%s/php${version}"
`, bin),
	})
	if _, ok := cfg.Apps["php"]; ok {
		t.Fatalf("php should not materialize when versions.unversioned is false")
	}
	if _, ok := cfg.Apps["php8.4"]; !ok {
		t.Fatalf("php8.4 should materialize")
	}
}

func TestVersionTemplateUnversionedCanOverrideMetadata(t *testing.T) {
	bin := makeBinDir(t, "php")
	cfg := loadCatalog(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/php.yml": fmt.Sprintf(`
name: php%%v
display_name: "PHP ${version}"
description: "PHP runtime ${version}"
versions:
  unversioned:
    display_name: "System PHP"
    description: "Default PHP interpreter"
variables:
  binary: "%s/php${version}"
`, bin),
	})
	doc, ok := cfg.Apps["php"]
	if !ok {
		t.Fatalf("php should materialize")
	}
	if got := DisplayName(doc.Body, "php"); got != "System PHP" {
		t.Fatalf("php display_name = %q, want System PHP", got)
	}
	if got := cfgval.String(doc.Body["description"]); got != "Default PHP interpreter" {
		t.Fatalf("php description = %q, want Default PHP interpreter", got)
	}
}

func TestVersionTemplateSkipsExistingCanonicalName(t *testing.T) {
	bin := makeBinDir(t, "python3")
	cfg := loadCatalog(t, map[string]string{
		"sermo.yml": baseGlobal,
		"catalog/apps/python%n.yml": fmt.Sprintf(`
name: python%%n
display_name: "Python ${n}"
variables:
  binary: "%s/python${n}"
`, bin),
		"catalog/apps/python3.yml": fmt.Sprintf(`
name: python3
display_name: "Python Three"
variables:
  binary: "%s/python3"
`, bin),
	})
	if got := strings.Join(cfg.CatalogNamesInCategory(CategoryApp), ","); got != "python3" {
		t.Fatalf("app names = %s, want python3", got)
	}
	if got := DisplayName(cfg.Apps["python3"].Body, "python3"); got != "Python Three" {
		t.Fatalf("python3 display_name = %q, want explicit canonical app", got)
	}
}

func TestInstanceTemplateMaterialization(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(catalogDir, "services"), "openvpn-base.yml", `
name: openvpn
display_name: OpenVPN
service: openvpn
variables: { port: 1194 }
checks:
  port: { type: openvpn, port: "${port}" }
`)
	writeFile(t, filepath.Join(catalogDir, "apps"), "openvpn.yml", `
name: openvpn
display_name: OpenVPN
variables:
  binary: /usr/bin/openvpn
preflight:
  binary: { type: binary, path: "${binary}" }
`)
	tmpl := `
name: openvpn-%i
uses: openvpn
display_name: "OpenVPN ${instance}"
service: "openvpn.${instance}"
apps: [openvpn]
variables:
  config: "/etc/openvpn/${instance}.conf"
`
	writeFile(t, filepath.Join(catalogDir, "services"), "openvpn.yml", tmpl)
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: openrc }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global, withServiceUnits("openrc", []string{"openvpn.tun1", "openvpn.client-a"}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.CatalogServices["openvpn-%i"]; ok {
		t.Fatal("template openvpn-%i should not be registered")
	}
	for _, inst := range []string{"client-a", "tun1"} {
		name := "openvpn-" + inst
		doc, ok := cfg.CatalogServices[name]
		if !ok {
			t.Fatalf("expected materialized service %q", name)
		}
		if got := ServiceUnit(doc.Body, name); got != "openvpn."+inst {
			t.Fatalf("%s service unit = %q, want openvpn.%s", name, got, inst)
		}
		if got := DisplayName(doc.Body, name); got != "OpenVPN "+inst {
			t.Fatalf("%s display_name = %q, want OpenVPN %s", name, got, inst)
		}
		vars := nested(t, doc.Body, "variables")
		if got := cfgval.String(vars["config"]); got != "/etc/openvpn/"+inst+".conf" {
			t.Fatalf("%s config = %q, want /etc/openvpn/%s.conf", name, got, inst)
		}
		if _, ok := nested(t, doc.Body, "checks")["port"]; !ok {
			t.Fatalf("%s did not inherit base checks", name)
		}
	}
}

func TestInstanceTemplateMaterializesConfiguredFailedService(t *testing.T) {
	tests := []struct {
		name        string
		backend     string
		serviceUnit string
		activeUnit  string
	}{
		{
			name:        "systemd",
			backend:     "systemd",
			serviceUnit: "nebula@${instance}",
			activeUnit:  "nebula@nebula1.service",
		},
		{
			name:        "openrc",
			backend:     "openrc",
			serviceUnit: "nebula.${instance}",
			activeUnit:  "nebula.nebula1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertInstanceTemplateMaterializesConfiguredFailedService(t, tt.backend, tt.serviceUnit, tt.activeUnit)
		})
	}
}

func TestConfiguredServiceTemplateSkipsInactiveBackend(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": `
engine: { backend: systemd }
paths: { services: [@ROOT@/services], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`,
		"catalog/services/openvpn.yml": `
name: openvpn%s%i
service:
  openrc: ["openvpn.${instance}"]
`,
		"catalog/services/openvpn-client.yml": `
name: openvpn-client-%i
service:
  systemd: ["openvpn-client@${instance}"]
`,
		"services/openvpn-client-tun1.yml": `
name: openvpn-client-tun1
uses: openvpn-client-tun1
`,
	})
	cfg, err := loadConfig(t, global, withServiceUnits("systemd", []string{"openvpn-client@tun1.service"}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate() issues = %v", issues)
	}
	if _, ok := cfg.CatalogServices["openvpn-client-tun1"]; !ok {
		t.Fatal("systemd OpenVPN client instance must materialize")
	}
	if _, errs := cfg.Resolve("openvpn-client-tun1"); len(errs) > 0 {
		t.Fatalf("Resolve(openvpn-client-tun1) errors = %v", errs)
	}
}

func assertInstanceTemplateMaterializesConfiguredFailedService(t *testing.T, backend, serviceUnit, activeUnit string) {
	t.Helper()
	root := t.TempDir()
	catalogDir := filepath.Join(root, "catalog", "services")
	servicesDir := filepath.Join(root, "services")
	for _, dir := range []string{catalogDir, servicesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "nebula.yml"), fmt.Appendf(nil, `
name: nebula-%%i
service: %s
checks:
  service: { type: service, expect: active }
`, serviceUnit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(servicesDir, "nebula-nebula0.yml"), []byte(`
name: nebula-nebula0
uses: nebula-nebula0
`), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: %s }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, backend, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global,
		WithCatalogDirs(filepath.Dir(catalogDir)),
		withServiceUnits(backend, []string{activeUnit}),
	)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate() issues = %v", issues)
	}
	resolved, errs := cfg.Resolve("nebula-nebula0")
	if len(errs) != 0 {
		t.Fatalf("Resolve(nebula-nebula0) errors = %v", errs)
	}
	wantUnit := strings.ReplaceAll(serviceUnit, "${instance}", "nebula0")
	if got := ServiceUnit(resolved.Tree, "nebula-nebula0"); got != wantUnit {
		t.Fatalf("resolved unit = %q, want %q", got, wantUnit)
	}
	if _, ok := cfg.CatalogServices["nebula-nebula1"]; !ok {
		t.Fatal("active instance nebula-nebula1 must still materialize from the unit inventory")
	}
}

// TestVersionTemplateMaterialization exercises a `name: foo-%v` service template:
// it must produce one service per installed app version, inherit a `uses` base,
// and drop the template itself.
func TestVersionTemplateMaterialization(t *testing.T) {
	root := t.TempDir()
	binRoot := filepath.Join(root, "opt")
	makeVersionedBinaries(t, binRoot, "php", "php-fpm", "7.4", "8.3")

	catalogDir := filepath.Join(root, "catalog")
	catalogServicesDir := filepath.Join(catalogDir, "services")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Rich base with a marker rule and an extra variable, to prove inheritance.
	writeFile(t, catalogServicesDir, "php-fpm-base.yml", `
name: php-fpm
display_name: "PHP-FPM"
service: php-fpm
variables:
  binary: /usr/sbin/php-fpm
  user: www-data
rules:
  block-bad-config:
    type: guard
    blocks: [restart]
    if:
      failed:
        check: config
    then:
      action: block
      message: "${display_name} configuration is invalid"
`)
	writeFile(t, filepath.Join(catalogDir, "apps"), "php-fpm.yml", fmt.Sprintf(`
name: php-fpm-%%v
display_name: "PHP-FPM ${version}"
variables:
  binary: "%s/php${version}/bin/php-fpm"
preflight:
  binary: { type: binary, path: "${binary}" }
  version: { type: command, command: ["${binary}", "-v"] }
`, binRoot))
	// Version template inheriting the base; installed versions come from the app.
	writeFile(t, catalogServicesDir, "php-fpm-template.yml", `
name: php-fpm-%v
uses: php-fpm
display_name: "PHP-FPM ${version}"
apps: ["php-fpm-${version}"]
`)

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: auto }
paths:
  services: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Template must be gone; one concrete service per installed version present.
	if _, ok := cfg.CatalogServices["php-fpm-%v"]; ok {
		t.Errorf("template php-fpm-%%v should not be registered")
	}
	for _, v := range []string{"7.4", "8.3"} {
		name := "php-fpm-" + v
		doc, ok := cfg.CatalogServices[name]
		if !ok {
			t.Fatalf("expected materialized service %q", name)
		}
		// display_name has the version baked in (no literal ${version}).
		if got := DisplayName(doc.Body, name); got != "PHP-FPM "+v {
			t.Errorf("%s display_name = %q, want %q", name, got, "PHP-FPM "+v)
		}
		// Inherited the base rule; the versioned binary belongs to the linked app.
		wantBin := fmt.Sprintf("%s/php%s/bin/php-fpm", binRoot, v)
		if got := DocumentBinary(doc.Body); got != "/usr/sbin/php-fpm" {
			t.Errorf("%s binary = %q, want inherited /usr/sbin/php-fpm", name, got)
		}
		appDoc, ok := cfg.Apps[name]
		if !ok {
			t.Fatalf("expected materialized app %q", name)
		}
		if got := DocumentBinary(appDoc.Body); got != wantBin {
			t.Errorf("%s app binary = %q, want %q", name, got, wantBin)
		}
		if _, ok := nested(t, doc.Body, "rules")["block-bad-config"]; !ok {
			t.Errorf("%s did not inherit base rule", name)
		}
	}

	// A service using a materialized version resolves end to end, including the
	// inherited rule message expanding through the baked display_name.
	writeFile(t, servicesDir, "site.yml", `
name: site
uses: php-fpm-8.3
service: php-fpm
`)
	cfg, err = loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() reload error = %v", err)
	}
	resolved, errs := cfg.Resolve("site")
	if len(errs) != 0 {
		t.Fatalf("Resolve(site) errors = %v", errs)
	}
	then := nested(t, resolved.Tree, "rules", "block-bad-config", "then")
	if got := cfgval.String(then["message"]); got != "PHP-FPM 8.3 configuration is invalid" {
		t.Errorf("message = %q, want %q", got, "PHP-FPM 8.3 configuration is invalid")
	}
	binaryCheck := nested(t, resolved.Tree, "preflight", "php-fpm-8.3-binary")
	wantBinary := binRoot + "/php8.3/bin/php-fpm"
	if got := cfgval.String(binaryCheck["path"]); got != wantBinary {
		t.Errorf("linked app binary path = %q, want %q", got, wantBinary)
	}
}

func TestVersionTemplateCephOSD(t *testing.T) {
	root := t.TempDir()
	// Catalog files take their kind from the subdirectory (services/ → service,
	// apps/ → app), so the template and its app must live in the right dirs.
	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	writeFile(t, filepath.Join(catalogDir, "apps"), "ceph-osd.yml", `
name: ceph-osd
display_name: "Ceph OSD"
variables:
  binary: /usr/bin/ceph-osd
preflight:
  binary: { type: binary, path: "${binary}" }
`)
	writeFile(t, filepath.Join(catalogDir, "services"), "ceph-osd-%n.yml", `
name: ceph-osd%n
display_name: "Ceph OSD ${n}"
service: "ceph-osd@${n}"
apps: [ceph-osd]
variables: { user: ceph }
checks: { service: { type: service, expect: active } }
`)
	// One enabled service per OSD that uses the materialized service.
	writeFile(t, servicesDir, "osd0.yml", "name: osd0\nuses: ceph-osd0\n")

	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: systemd }
paths:
  services: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global, withServiceUnits("systemd", []string{
		"ceph-osd@0.service",
		"ceph-osd@1.service",
		"ceph-osd@3.service",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// Template gone; one concrete service per active OSD unit; absent id 2 not present.
	if _, ok := cfg.CatalogServices["ceph-osd%n"]; ok {
		t.Errorf("template ceph-osd%%n should not be registered")
	}
	if _, ok := cfg.CatalogServices["ceph-osd2"]; ok {
		t.Errorf("ceph-osd2 must not exist (no active ceph-osd@2.service)")
	}
	for _, id := range []string{"0", "1", "3"} {
		name := "ceph-osd" + id
		doc, ok := cfg.CatalogServices[name]
		if !ok {
			t.Fatalf("expected materialized service %q", name)
		}
		// ${n} baked into the unit name at materialization.
		if got := ServiceUnit(doc.Body, name); got != "ceph-osd@"+id {
			t.Errorf("%s service unit = %q, want ceph-osd@%s", name, got, id)
		}
	}
	// The app link survives materialization: a service using ceph-osd0 resolves
	// cleanly (the generic ceph-osd app's preflight binary check is wired in).
	resolved, errs := cfg.Resolve("osd0")
	if len(errs) != 0 {
		t.Fatalf("Resolve(osd0) errors = %v", errs)
	}
	if _, ok := resolved.Tree["preflight"].(map[string]any); !ok {
		t.Errorf("osd0 missing preflight from linked ceph-osd app: %v", resolved.Tree)
	}
}

func TestVersionTemplateCephOSDNoMatch(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "catalog")
	catalogServicesDir := filepath.Join(catalogDir, "services")
	servicesDir := filepath.Join(root, "services")
	for _, d := range []string{catalogServicesDir, servicesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	appsDir := filepath.Join(catalogDir, "apps")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appsDir, "ceph-osd.yml"), []byte(`
name: ceph-osd
variables:
  binary: /usr/bin/ceph-osd
preflight:
  binary: { type: binary, path: "${binary}" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogServicesDir, "ceph-osd-%n.yml"), []byte(`
name: ceph-osd%n
service: "ceph-osd@${n}"
apps: [ceph-osd]
variables: { user: ceph }
checks: { service: { type: service, expect: active } }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: systemd }
paths:
  services: [ %s ]
  runtime: /run/sermo
defaults:
  policy: { cooldown: 5m }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(t, global, withServiceUnits("systemd", nil))
	if err != nil {
		t.Fatalf("Load() with no OSDs must not error, got %v", err)
	}
	for name := range cfg.CatalogServices {
		if strings.HasPrefix(name, "ceph-osd") {
			t.Errorf("no ceph-osd services expected with zero discovery matches, got %q", name)
		}
	}
}

// TestCatalogServiceOwnsDiscovery covers the v2 rule: a catalog service template that declares
// its own token-bearing `versions.from` materializes from that path directly,
// without needing a linked discovery app.
func TestCatalogServiceOwnsDiscovery(t *testing.T) {
	root := t.TempDir()
	confd := filepath.Join(root, "conf")
	if err := os.MkdirAll(confd, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"myd-tun1.conf", "myd-tun2.conf"} {
		if err := os.WriteFile(filepath.Join(confd, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(catalogDir, "services"), "myd-%i.yml", fmt.Sprintf(`
name: myd-%%i
display_name: "Myd ${instance}"
service: "myd.${instance}"
versions:
  from: "%s/myd-${instance}.conf"
checks:
  service: { type: service, expect: active }
`, confd))
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: auto }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.CatalogServices["myd-%i"]; ok {
		t.Fatal("template myd-%i should not be registered")
	}
	for _, inst := range []string{"tun1", "tun2"} {
		name := "myd-" + inst
		doc, ok := cfg.CatalogServices[name]
		if !ok {
			t.Fatalf("expected materialized service %q from service-owned discovery", name)
		}
		if got := ServiceUnit(doc.Body, name); got != "myd."+inst {
			t.Fatalf("%s service unit = %q, want myd.%s", name, got, inst)
		}
	}
}

// TestMultiTokenSeparatorMaterialization covers a `name: tomcat-%v%s%i` template:
// version + optional separator + instance discovered together from service units.
// The no-instance case (tomcat-8.5) must materialize without a trailing separator.
func TestMultiTokenSeparatorMaterialization(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "catalog")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(catalogDir, "services")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tomcat-%v%s%i.yml"), fmt.Appendf(nil, `
name: tomcat-%%v%%s%%i
display_name: "Tomcat ${version} (${instance})"
service: "tomcat-${version}${sep}${instance}"
variables:
  config: "/etc/tomcat-${version}${sep}${instance}/server.xml"
checks:
  service: { type: service, expect: active }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: systemd }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global, withServiceUnits("systemd", []string{
		"tomcat-8.5-main.service",
		"tomcat-9-guacamole.service",
		"tomcat-8.5.service",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := map[string]string{
		"tomcat-8.5-main":    "tomcat-8.5-main",
		"tomcat-9-guacamole": "tomcat-9-guacamole",
		"tomcat-8.5":         "tomcat-8.5", // no instance -> no trailing separator
	}
	for name, unit := range want {
		doc, ok := cfg.CatalogServices[name]
		if !ok {
			t.Fatalf("expected materialized service %q", name)
		}
		if got := ServiceUnit(doc.Body, name); got != unit {
			t.Fatalf("%s service unit = %q, want %q", name, got, unit)
		}
	}
	if _, ok := cfg.CatalogServices["tomcat-8.5-"]; ok {
		t.Fatal("must not materialize a trailing-separator name tomcat-8.5-")
	}
}

// TestMultiTokenDiscoveryRequireGate covers `versions.require`: an instance
// discovered from config (php-fpm pools, tomcat envs) is materialized only when
// its required binary also exists, so a stray config directory whose runtime is
// not installed does not produce a service with a dangling app link.
func TestMultiTokenDiscoveryRequireGate(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	bin := filepath.Join(root, "bin")
	for _, d := range []string{"app8.4", "app8.4_pool", "app5.6"} {
		if err := os.MkdirAll(filepath.Join(etc, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Binary present only for 8.4 (so 8.4 and 8.4_pool keep it; 5.6 is gated out).
	if err := os.WriteFile(filepath.Join(bin, "app8.4"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	catalogDir := filepath.Join(root, "catalog", "services")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "app%v%s%i.yml"), fmt.Appendf(nil, `
name: app%%v%%s%%i
service: "app${version}${sep}${instance}"
versions:
  from: "%s/app${version}${sep}${instance}"
  require: "%s/app${version}"
checks:
  service: { type: service, expect: active }
`, etc, bin), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: auto }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	for _, name := range []string{"app8.4", "app8.4_pool"} {
		if _, ok := cfg.CatalogServices[name]; !ok {
			t.Errorf("expected %q to materialize (binary present)", name)
		}
	}
	if _, ok := cfg.CatalogServices["app5.6"]; ok {
		t.Error("app5.6 must be gated out: its required binary is absent")
	}
}

// TestSingleTokenDiscoveryRequireGate keeps overlapping one-token unit names in
// their own catalog profile unless the matching Nebula-style config exists.
func TestSingleTokenDiscoveryRequireGate(t *testing.T) {
	root := t.TempDir()
	candidates := filepath.Join(root, "candidates")
	required := filepath.Join(root, "required")
	for _, dir := range []string{candidates, required} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"app1", "app2"} {
		if err := os.WriteFile(filepath.Join(candidates, name), []byte("enabled: true\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(required, "app1.yml"), []byte("enabled: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalogDir := filepath.Join(root, "catalog", "services")
	servicesDir := filepath.Join(root, "services")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogDir, "app%v.yml"), fmt.Appendf(nil, `
name: app%%v
service: "app${version}"
versions:
  from: "%s/app${version}"
  require:
    - "%s/app${version}.yml"
checks:
  service: { type: service, expect: active }
`, candidates, required), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, fmt.Appendf(nil, `
engine: { backend: auto }
paths: { services: [ %s ], runtime: /run/sermo }
defaults: { policy: { cooldown: 5m } }
`, servicesDir), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, ok := cfg.CatalogServices["app1"]; !ok {
		t.Error("app1 must materialize when its required config exists")
	}
	if _, ok := cfg.CatalogServices["app2"]; ok {
		t.Error("app2 must be gated out: its required config is absent")
	}
}
