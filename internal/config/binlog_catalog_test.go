package config

import (
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
)

// TestBinlogCatalogWatch: the mysql and mariadb profiles size their binary logs
// every binlog_check_interval, and an override that empties the growth
// variables keeps only the max limit instead of failing validation.
func TestBinlogCatalogWatch(t *testing.T) {
	const watch = "alert-if-binlog-large"
	for _, profile := range []string{"mariadb", "mysql"} {
		for _, maxOnly := range []bool{false, true} {
			name, body := profile+"/default", "name: "+profile+"\nuses: "+profile+"\n"
			if maxOnly {
				name = profile + "/max-only"
				body += "variables:\n  binlog_grow_by: \"\"\n  binlog_grow_within: \"\"\n"
			}
			t.Run(name, func(t *testing.T) {
				global := writeConfig(t, map[string]string{
					"sermo.yml":                    "engine: {backend: openrc}\npaths: {services: [@ROOT@/services]}\ndefaults: {policy: {cooldown: 5m}}\n",
					"services/" + profile + ".yml": body,
				})
				cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(repoRoot(t))))
				if err != nil {
					t.Fatal(err)
				}
				if issues := Validate(cfg); len(issues) != 0 {
					t.Fatal(issues)
				}
				resolved, errs := cfg.Resolve(profile)
				if len(errs) != 0 {
					t.Fatal(errs)
				}
				check := nested(t, resolved.Tree, "checks", watch)
				if got := cfgval.String(check[keyInterval]); got != "5m" {
					t.Fatalf("interval = %q, want the 5m binlog_check_interval", got)
				}
				if got := cfgval.String(check[checks.CheckKeyPath]); got != "/var/lib/mysql/*bin*.[0-9]*" {
					t.Fatalf("path = %q", got)
				}
				if got := cfgval.String(check[checks.CheckKeyMax]); got != "20G" {
					t.Fatalf("max = %q, want 20G", got)
				}
				growBy, within := cfgval.String(check[checks.CheckKeyGrowBy]), cfgval.String(check[checks.CheckKeyWithin])
				if maxOnly && (growBy != "" || within != "") {
					t.Fatalf("emptied growth variables must leave no window: grow_by=%q within=%q", growBy, within)
				}
				if !maxOnly && (growBy != "1G" || within != "30m") {
					t.Fatalf("grow_by/within = %q/%q, want 1G/30m", growBy, within)
				}
			})
		}
	}
}
