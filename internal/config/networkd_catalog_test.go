package config

import (
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/process"
)

func TestCatalogNetworkdReactivationIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		user string
	}{
		{name: "default account", user: "systemd-network"},
		{name: "custom account", user: "network"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := "name: network\nuses: systemd-networkd\n"
			if tc.user != "systemd-network" {
				body += "variables: {user: " + tc.user + "}\n"
			}
			global := writeConfig(t, map[string]string{
				"sermo.yml":                       "engine: {backend: systemd}\npaths: {services: [@ROOT@/services], apps: [@ROOT@/apps]}\ndefaults: {policy: {cooldown: 5m}}\n",
				"services/network.yml":            body,
				"apps.local/systemd-networkd.yml": "name: systemd-networkd\nvariables: {binary: /opt/systemd/systemd-networkd}\n",
			})
			cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(repoRoot(t))))
			if err != nil {
				t.Fatal(err)
			}
			if issues := Validate(cfg); len(issues) != 0 {
				t.Fatal(issues)
			}
			resolved, errs := cfg.Resolve("network")
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			selectors, warnings := process.ParseSelectors(resolved.Tree)
			if len(warnings) != 0 || len(selectors) != 1 {
				t.Fatalf("selectors = %v, warnings = %v", selectors, warnings)
			}
			main := selectors[0]
			if main.Name != process.RoleMain || !main.IsStrictPrincipal() ||
				main.Exe != "/opt/systemd/systemd-networkd" || main.User != tc.user {
				t.Fatalf("networkd must prove its principal identity: %+v", main)
			}
			binary := nested(t, resolved.Tree, "preflight", "systemd-networkd-binary")
			if cfgval.String(binary["path"]) != main.Exe || cfgval.String(binary["type"]) != "binary" {
				t.Fatalf("binary preflight must validate the process executable: %v", binary)
			}
		})
	}
}
