package config

import (
	"slices"
	"testing"

	"sermo/internal/cfgval"
)

// TestCatalogSmokepingIsAttributedByItsUnit pins how the SmokePing profile
// finds its daemon. SmokePing is a Perl program that renames its process, so an
// exe selector would name the shared interpreter: the profile declares none and
// lets the init unit attribute it, and it links perl so an interpreter upgrade
// renews it.
func TestCatalogSmokepingIsAttributedByItsUnit(t *testing.T) {
	smokeping := catalogDocByName(t, repoRoot(t), "services", "smokeping")
	if _, declared := smokeping["processes"]; declared {
		t.Fatalf("smokeping declares process selectors: %v", smokeping["processes"])
	}
	if apps := cfgval.StringList(smokeping["apps"]); !slices.Contains(apps, "perl") || !slices.Contains(apps, "smokeping") {
		t.Fatalf("smokeping apps = %v, want perl and smokeping", apps)
	}
	resolved := resolveCatalogService(t, "smokeping", backendOpenRC)
	if got := cfgval.String(resolved.Tree["pidfile"]); got != "/run/smokeping/smokeping.pid" {
		t.Fatalf("smokeping pidfile = %q, want /run/smokeping/smokeping.pid", got)
	}
}
