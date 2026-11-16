package config

import (
	"slices"
	"strings"
	"testing"

	"sermo/internal/cfgval"
)

// OpenRC runs a second vinylncsa logger and a user's PM2 from links to the
// packaged script (vinylncsa.backend, pm2.apache). Each materializes from its
// instance template with its own unit, and takes the base profile's checks.
func TestCatalogOpenRCLinkInstancesMaterialize(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml":            "engine: {backend: openrc}\npaths: {services: [@ROOT@/services]}\ndefaults: {policy: {cooldown: 5m}}\n",
		"services/backend.yml": "name: vinylncsa.backend\nuses: vinylncsa.backend\n",
		"services/pm2.yml":     "name: pm2.apache\nuses: pm2.apache\n",
	})
	cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(repoRoot(t))),
		withServiceUnits("openrc", []string{"vinylncsa", "vinylncsa.backend", "pm2.apache"}))
	if err != nil {
		t.Fatal(err)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatal(issues)
	}

	logger, errs := cfg.Resolve("vinylncsa.backend")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if got := serviceUnitFor(logger.Tree, backendOpenRC, "vinylncsa.backend"); got != "vinylncsa.backend" {
		t.Fatalf("logger unit = %q, want vinylncsa.backend", got)
	}
	if got := cfgval.String(logger.Tree["pidfile"]); got != "/run/vinylncsa.backend.pid" {
		t.Fatalf("logger pidfile = %q, want the instance's own", got)
	}
	if got := cfgval.String(nested(t, logger.Tree, "processes", "main")["cmd"]); got != " -P /run/vinylncsa.backend.pid( |$)" {
		t.Fatalf("logger selector cmd = %q, want the instance's -P pidfile", got)
	}
	if _, ok := nested(t, logger.Tree, "checks")["restart-if-inactive"]; !ok {
		t.Fatal("logger did not inherit the base profile's restart check")
	}

	manager, errs := cfg.Resolve("pm2.apache")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if got := serviceUnitFor(manager.Tree, backendOpenRC, "pm2.apache"); got != "pm2.apache" {
		t.Fatalf("pm2 unit = %q, want pm2.apache", got)
	}
	main := nested(t, manager.Tree, "processes", "main")
	if got := cfgval.String(main["user"]); got != "apache" {
		t.Fatalf("pm2 selector user = %q, want the user the link names", got)
	}
	daemon := nested(t, manager.Tree, "checks", "god-daemon")
	if got := cfgval.String(daemon["user"]); got != "apache" || cfgval.String(daemon["cmd"]) != cfgval.String(main["cmd"]) {
		t.Fatalf("pm2 god-daemon check = %v, want the selector's user and command", daemon)
	}
	// A node upgrade leaves the live daemon on a deleted executable no exe matches.
	if _, ok := daemon["exe"]; ok {
		t.Fatalf("pm2 god-daemon check names an executable: %v", daemon)
	}
	// The home comes from the host's passwd, so only its shape is asserted.
	pidfile := cfgval.String(manager.Tree["pidfile"])
	if !strings.HasSuffix(pidfile, "/.pm2/pm2.pid") || slices.Contains([]string{"/run/pm2.pid", "/root/.pm2/pm2.pid"}, pidfile) {
		t.Fatalf("pm2 instance pidfile = %q, want the user's own ~/.pm2, never root's", pidfile)
	}
}

// The instance templates discover only from their OpenRC links. On systemd,
// root's pm2-root unit stays the base profile's, never a second pm2.root.
func TestCatalogOpenRCLinkInstancesStayOffSystemd(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml":        "engine: {backend: systemd}\npaths: {services: [@ROOT@/services]}\ndefaults: {policy: {cooldown: 5m}}\n",
		"services/pm2.yml": "name: pm2\nuses: pm2\n",
	})
	cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(repoRoot(t))),
		withServiceUnits("systemd", []string{"pm2-root.service", "vinylncsa.service"}))
	if err != nil {
		t.Fatal(err)
	}
	for name := range cfg.CatalogServices {
		if strings.HasPrefix(name, "pm2.") || strings.HasPrefix(name, "vinylncsa.") {
			t.Fatalf("systemd host materialized %q from an OpenRC link template", name)
		}
	}
}
