package config

import (
	"testing"

	"sermo/internal/cfgval"
)

// TestNamedCatalogRecursionRequiresOptIn: the named profile's `recursion` and
// `resolver` watches stay off by default (an authoritative-only server would
// answer REFUSED) and resolve into service checks once an instance enables them.
func TestNamedCatalogRecursionRequiresOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "default"
		body := "name: named\nuses: named\n"
		if enabled {
			name = "enabled"
			body += "watches:\n  recursion: {enabled: true}\n  resolver: {enabled: true}\n"
		}
		t.Run(name, func(t *testing.T) {
			global := writeConfig(t, map[string]string{
				"sermo.yml":          "engine: {backend: openrc}\npaths: {services: [@ROOT@/services]}\ndefaults: {policy: {cooldown: 5m}}\n",
				"services/named.yml": body,
			})
			cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(repoRoot(t))))
			if err != nil {
				t.Fatal(err)
			}
			if issues := Validate(cfg); len(issues) != 0 {
				t.Fatal(issues)
			}
			resolved, errs := cfg.Resolve("named")
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			checks := nested(t, resolved.Tree, "checks")
			_, hasRecursion := checks["recursion"]
			_, hasResolver := checks["resolver"]
			if hasRecursion != enabled || hasResolver != enabled {
				t.Fatalf("opt-in=%v: recursion=%v resolver=%v", enabled, hasRecursion, hasResolver)
			}
			if _, hasPort := checks["port"]; !hasPort {
				t.Fatal("the liveness probe must stay on")
			}
			if !enabled {
				return
			}
			recursion := nested(t, resolved.Tree, "checks", "recursion")
			if got := cfgval.String(recursion["query"]); got != "example.com" {
				t.Fatalf("recursion query = %q, want the recursion_query default", got)
			}
			if got := cfgval.String(recursion["host"]); got != "127.0.0.1" {
				t.Fatalf("recursion host = %q, want the profile host", got)
			}
			resolver := nested(t, resolved.Tree, "checks", "resolver")
			if !cfgval.Bool(resolver["resolvconf"]) || cfgval.String(resolver["host"]) != "" {
				t.Fatalf("resolver must probe through resolv.conf without a host: %v", resolver)
			}
		})
	}
}
