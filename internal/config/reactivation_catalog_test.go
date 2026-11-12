package config

import (
	"fmt"
	"slices"
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/process"
)

func TestCatalogReactivationIdentity(t *testing.T) {
	t.Parallel()
	for _, service := range []struct {
		name, app, user string
	}{
		{"systemd-networkd", "systemd-networkd", "systemd-network"},
		{"systemd-resolved", "systemd-resolved", "systemd-resolve"},
		{"systemd-logind", "systemd-logind", "root"},
		{"systemd-machined", "systemd-machined", "root"},
		{"bluetooth", "bluetoothd", "root"},
	} {
		for _, user := range []string{service.user, "custom-daemon"} {
			t.Run(service.name+"/"+user, func(t *testing.T) {
				t.Parallel()
				body := "name: fixture\nuses: " + service.name + "\n"
				if user != service.user {
					body += "variables: {user: " + user + "}\n"
				}
				exe := "/opt/fixture/" + service.app
				global := writeConfig(t, map[string]string{
					"sermo.yml":                          "engine: {backend: systemd}\npaths: {services: [@ROOT@/services], apps: [@ROOT@/apps]}\ndefaults: {policy: {cooldown: 5m}}\n",
					"services/fixture.yml":               body,
					"apps.local/" + service.app + ".yml": fmt.Sprintf("name: %s\nvariables: {binary: %s}\n", service.app, exe),
				})
				cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(repoRoot(t))))
				if err != nil {
					t.Fatal(err)
				}
				if issues := Validate(cfg); len(issues) != 0 {
					t.Fatal(issues)
				}
				resolved, errs := cfg.Resolve("fixture")
				if len(errs) != 0 {
					t.Fatal(errs)
				}
				selectors, warnings := process.ParseSelectors(resolved.Tree)
				principal := slices.IndexFunc(selectors, func(s process.Selector) bool { return s.Name == process.RoleMain })
				if len(warnings) != 0 || principal < 0 {
					t.Fatalf("selectors = %v, warnings = %v", selectors, warnings)
				}
				main := selectors[principal]
				if !main.IsStrictPrincipal() ||
					main.Exe != exe || main.User != user {
					t.Fatalf("reactivation must prove its principal identity: %+v", main)
				}
				binary := nested(t, resolved.Tree, "preflight", service.app+"-binary")
				if cfgval.String(binary["path"]) != main.Exe || cfgval.String(binary["type"]) != "binary" {
					t.Fatalf("binary preflight must validate the process executable: %v", binary)
				}
			})
		}
	}
}
