package config

import "testing"

func TestPHPFPMCatalogDisabledProbeValidates(t *testing.T) {
	for _, backend := range []string{"systemd", "openrc"} {
		t.Run(backend, func(t *testing.T) {
			global := writeConfig(t, map[string]string{
				"sermo.yml":        "engine: {backend: " + backend + "}\npaths: {services: [@ROOT@/services]}\ndefaults: {policy: {cooldown: 5m}}\n",
				"services/php.yml": "name: php-fpm8.4\nuses: php-fpm8.4\nwatches:\n  fpm: {enabled: false}\n",
			})
			cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(repoRoot(t))))
			if err != nil {
				t.Fatal(err)
			}
			if issues := Validate(cfg); len(issues) != 0 {
				t.Fatal(issues)
			}
		})
	}
}
