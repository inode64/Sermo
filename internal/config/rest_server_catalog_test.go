package config

import (
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/process"
	"sermo/internal/rules"
)

func TestCatalogRestServerProcessIdentity(t *testing.T) {
	for _, backend := range []string{backendSystemd, backendOpenRC} {
		for _, user := range []string{"root", "restic"} {
			t.Run(backend+"/"+user, func(t *testing.T) {
				body := "name: backups\nuses: rest-server\nvariables:\n  rest_server_binary: /opt/restic/rest-server\n"
				if user != "root" {
					body += "  user: " + user + "\n"
				}
				global := writeConfig(t, map[string]string{
					"sermo.yml":            "engine: {backend: " + backend + "}\npaths: {services: [@ROOT@/services]}\ndefaults: {policy: {cooldown: 5m}}\n",
					"services/backups.yml": body,
				})
				cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(repoRoot(t))))
				if err != nil {
					t.Fatal(err)
				}
				if issues := Validate(cfg); len(issues) != 0 {
					t.Fatal(issues)
				}
				resolved, errs := cfg.Resolve("backups")
				if len(errs) != 0 {
					t.Fatal(errs)
				}
				selectors, warnings := process.ParseSelectors(resolved.Tree)
				if len(warnings) != 0 || len(selectors) != 1 {
					t.Fatalf("selectors = %v, warnings = %v", selectors, warnings)
				}
				selector := selectors[0]
				if selector.Exe != "/opt/restic/rest-server" || selector.User != user {
					t.Fatalf("process identity = %v, want linked binary and user %s", selector, user)
				}
				if cfgval.String(nested(t, resolved.Tree, "checks", fdsCheckName)["value"]) != defaultFDsLimit {
					t.Fatal("FD coverage missing")
				}
				if cfgval.String(nested(t, resolved.Tree, "rules", fdsRuleName)["type"]) != string(rules.RuleAlert) {
					t.Fatal("FD saturation must remain alert only")
				}
				policy, policyWarnings := process.ParseStopPolicy(resolved.Tree)
				if len(policyWarnings) != 0 {
					t.Fatal(policyWarnings)
				}
				if policy.ForceKill {
					t.Fatal("process discovery must not authorize SIGKILL")
				}
			})
		}
	}
}
