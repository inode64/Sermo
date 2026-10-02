package config

import (
	"slices"
	"testing"

	"sermo/internal/cfgval"
)

// Workers must remain discoverable and authorized for residual cleanup after
// the master dies and its pidfile no longer anchors the process tree.
func TestPHPFPMCatalogWorkerIdentity(t *testing.T) {
	bindir := t.TempDir()
	fakeBinary(t, bindir, "php-fpm8.4")
	stubBinDirs(t, bindir)
	for _, tt := range []struct {
		name, os, override, want string
	}{
		{name: "gentoo", os: "gentoo", want: "apache"},
		{name: "debian", os: "debian", want: "www-data"},
		{name: "custom pool", os: "gentoo", override: "variables: {user: site}\n", want: "site"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			old := detectedOS
			detectedOS = tt.os
			t.Cleanup(func() { detectedOS = old })
			global := writeConfig(t, map[string]string{
				"sermo.yml":        "engine: {backend: openrc}\npaths: {services: [@ROOT@/services]}\ndefaults: {policy: {cooldown: 5m}}\n",
				"services/php.yml": "name: php-fpm8.4\nuses: php-fpm8.4\n" + tt.override,
			})
			cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(repoRoot(t))), withServiceUnits("openrc", nil))
			if err != nil {
				t.Fatal(err)
			}
			resolved, errs := cfg.Resolve("php-fpm8.4")
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			worker := nested(t, resolved.Tree, "processes", "workers")
			if worker["user"] != tt.want || worker["exe"] == "" {
				t.Fatalf("worker identity = %v, want exact executable and user %q", worker, tt.want)
			}
			kill := nested(t, resolved.Tree, "stop_policy", "kill_only_if")
			if !slices.Contains(cfgval.StringList(kill["users"]), tt.want) || !slices.Contains(cfgval.StringList(kill["exe_any"]), cfgval.String(worker["exe"])) {
				t.Fatalf("kill selector %v does not authorize worker identity %v", kill, worker)
			}
		})
	}
}

// TestPHPFPMCatalogDisabledProbeValidates checks that the shipped php-fpm
// template, with its fpm status probe disabled, validates on both init
// backends. The php-fpm8.4 instance comes from a fake binary under a stubbed
// ${bindir}, so the result does not depend on which PHP the host has installed.
// Validate covers the whole catalog, and its python services need the python3
// app template materialized the same way. Active-unit discovery is stubbed
// empty so a php-fpm unit running on the host cannot materialize a version
// the fake bindir does not carry.
func TestPHPFPMCatalogDisabledProbeValidates(t *testing.T) {
	bindir := t.TempDir()
	fakeBinary(t, bindir, "php-fpm8.4")
	fakeBinary(t, bindir, "python3")
	stubBinDirs(t, bindir)
	for _, backend := range []string{"systemd", "openrc"} {
		t.Run(backend, func(t *testing.T) {
			global := writeConfig(t, map[string]string{
				"sermo.yml":        "engine: {backend: " + backend + "}\npaths: {services: [@ROOT@/services]}\ndefaults: {policy: {cooldown: 5m}}\n",
				"services/php.yml": "name: php-fpm8.4\nuses: php-fpm8.4\nwatches:\n  fpm: {enabled: false}\n",
			})
			cfg, err := loadConfig(t, global,
				WithCatalogDirs(repoCatalogDir(repoRoot(t))),
				withServiceUnits(backend, nil))
			if err != nil {
				t.Fatal(err)
			}
			if issues := Validate(cfg); len(issues) != 0 {
				t.Fatal(issues)
			}
		})
	}
}
