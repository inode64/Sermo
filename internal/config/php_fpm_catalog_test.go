package config

import "testing"

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
